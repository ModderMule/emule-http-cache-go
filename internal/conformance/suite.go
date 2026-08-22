package conformance

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// partSize is one full eMule part — PARTSIZE from the client's Opcodes.h.
	partSize = 9_728_000

	// cipherSize is what that becomes after AES-CBC with PKCS#7. The part is an
	// exact multiple of the block size, so padding appends a whole extra block.
	cipherSize = 9_728_016
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Suite is one run of the contract test against a base URL.
type Suite struct {
	BaseURL string
	APIKey  string
	Client  *http.Client

	reporter Reporter
	result   Result

	plain  []byte
	cipher []byte
	key    []byte
	iv     []byte

	maxChunkSize       int64
	uploadRequiresAuth bool
	chunkID            string
	chunkURL           string
}

// Run executes every assertion and returns the tally.
func (s *Suite) Run(r Reporter) Result {
	s.reporter = r
	s.result = Result{}

	base := http.DefaultTransport
	if s.Client != nil && s.Client.Transport != nil {
		base = s.Client.Transport
	}
	client := &http.Client{
		Timeout:   2 * time.Minute,
		Transport: &tracer{next: base, reporter: r},
		// A chunk URL is used verbatim and a redirect on that path is a failed
		// fetch for the real client, so the suite never follows one either.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	s.Client = client

	r.Logf("base: %s", s.BaseURL)

	s.checkInfo()
	if !s.preparePayload() {
		return s.result
	}
	s.checkAuth()
	if !s.checkUpload() {
		return s.result
	}
	s.checkDownload()
	s.checkRanges()
	s.checkDelete()
	s.checkMisc()

	return s.result
}

// -- sections ----------------------------------------------------------------

func (s *Suite) checkInfo() {
	s.reporter.Section("/v1/info")

	resp, body, err := s.do("GET", s.BaseURL+"/v1/info", nil, nil)
	if err != nil {
		s.fail("reports service name", err.Error())
		return
	}
	_ = resp

	var info struct {
		Service            string `json:"service"`
		Version            int    `json:"version"`
		MaxChunkSize       int64  `json:"maxChunkSize"`
		RangeSupported     *bool  `json:"rangeSupported"`
		UploadRequiresAuth *bool  `json:"uploadRequiresAuth"`
	}
	_ = json.Unmarshal(body, &info)

	s.assert(info.Service == "emule-http-cache", "reports service name",
		"service name missing: "+string(body))
	s.assert(info.RangeSupported != nil && *info.RangeSupported, "advertises Range support",
		"rangeSupported missing")

	// Absent means required: a backend written before the field existed is a
	// closed one, and guessing the other way would skip a real assertion.
	s.uploadRequiresAuth = info.UploadRequiresAuth == nil || *info.UploadRequiresAuth

	s.maxChunkSize = info.MaxChunkSize
	s.assert(s.maxChunkSize >= cipherSize,
		fmt.Sprintf("maxChunkSize %d fits a part", s.maxChunkSize),
		fmt.Sprintf("%d < %d", s.maxChunkSize, cipherSize))
}

func (s *Suite) preparePayload() bool {
	s.reporter.Section("prepare payload")

	s.plain = make([]byte, partSize)
	s.key = make([]byte, 32)
	s.iv = make([]byte, 16)
	for _, buf := range [][]byte{s.plain, s.key, s.iv} {
		if _, err := rand.Read(buf); err != nil {
			s.fail("generate a payload", err.Error())
			return false
		}
	}

	block, err := aes.NewCipher(s.key)
	if err != nil {
		s.fail("build the cipher", err.Error())
		return false
	}

	padded := pkcs7Pad(s.plain, aes.BlockSize)
	s.cipher = make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, s.iv).CryptBlocks(s.cipher, padded)

	s.check("plaintext is one full part", len(s.plain), partSize)
	s.check("ciphertext is part + one pad block", len(s.cipher), cipherSize)

	return true
}

func (s *Suite) checkAuth() {
	s.reporter.Section("auth")

	resp, _, err := s.do("POST", s.BaseURL+"/v1/chunks", s.cipher, http.Header{
		"Authorization": {"Bearer definitely-not-the-key"},
		"Content-Type":  {"application/octet-stream"},
	})
	s.checkStatus("POST with a wrong key is rejected", resp, err, 401)

	// A few bytes rather than a whole part: on an open server this one is
	// stored, and an anonymous chunk cannot be deleted afterwards.
	resp, _, err = s.do("POST", s.BaseURL+"/v1/chunks", []byte("no-key-probe"), http.Header{
		"Content-Type": {"application/octet-stream"},
	})
	if s.uploadRequiresAuth {
		s.checkStatus("POST with no key is rejected", resp, err, 401)
	} else {
		// The wrong key above must still be a 401 even here: only an absent
		// credential falls through to anonymous.
		s.checkStatus("POST with no key is accepted on an open server", resp, err, 201)
	}

	// Chunked transfer omits Content-Length, which the contract requires.
	resp, _, err = s.doChunked("POST", s.BaseURL+"/v1/chunks", s.cipher, http.Header{
		"Authorization": {"Bearer " + s.APIKey},
		"Content-Type":  {"application/octet-stream"},
	})
	s.checkStatus("POST without Content-Length is rejected", resp, err, 411)
}

func (s *Suite) checkUpload() bool {
	s.reporter.Section("POST /v1/chunks")

	resp, body, err := s.do("POST", s.BaseURL+"/v1/chunks", s.cipher, http.Header{
		"Authorization": {"Bearer " + s.APIKey},
		"Content-Type":  {"application/octet-stream"},
		"X-Chunk-TTL":   {"600"},
	})
	if !s.checkStatus("returns 201", resp, err, 201) {
		return false
	}

	var created struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		Size    int64  `json:"size"`
		Expires int64  `json:"expires"`
	}
	_ = json.Unmarshal(body, &created)

	s.chunkID, s.chunkURL = created.ID, created.URL

	s.assert(s.chunkURL != "", "returns a url ("+s.chunkURL+")", "no url in: "+string(body))
	s.assert(idPattern.MatchString(s.chunkID), "id is 128 bits of hex", "bad id: "+s.chunkID)
	s.check("echoes the stored size", int(created.Size), cipherSize)

	// A duration here instead of a timestamp would make every offer built from
	// this response decline as BadOffer, and nothing else in the suite would
	// notice.
	s.assert(created.Expires > time.Now().Unix(),
		"expires is an absolute unix timestamp in the future",
		fmt.Sprintf("got %d, now is %d", created.Expires, time.Now().Unix()))

	if s.chunkURL == "" {
		s.fail("upload returned a url", "the rest of the suite cannot run")
		return false
	}

	return true
}

func (s *Suite) checkDownload() {
	s.reporter.Section("GET the chunk")

	resp, body, err := s.do("GET", s.chunkURL, nil, nil)
	if err != nil {
		s.fail("ciphertext round-trips byte for byte", err.Error())
		return
	}

	s.check("ciphertext round-trips byte for byte", digest(body), digest(s.cipher))
	s.assert(strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes"),
		"advertises Accept-Ranges", "no Accept-Ranges header")
	s.assert(strings.EqualFold(resp.Header.Get("Content-Type"), "application/octet-stream"),
		"serves application/octet-stream", "wrong Content-Type: "+resp.Header.Get("Content-Type"))

	// The one assertion that proves the whole point of the service: the bytes a
	// peer fetches decrypt back to the part the uploader published.
	block, err := aes.NewCipher(s.key)
	if err != nil || len(body)%aes.BlockSize != 0 || len(body) == 0 {
		s.fail("decrypts back to the original part", "the body is not a whole number of AES blocks")
		return
	}

	decrypted := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, s.iv).CryptBlocks(decrypted, body)

	unpadded, ok := pkcs7Unpad(decrypted, aes.BlockSize)
	s.assert(ok && digest(unpadded) == digest(s.plain), "decrypts back to the original part",
		"the plaintext does not match what was published")
}

func (s *Suite) checkRanges() {
	s.reporter.Section("Range requests")

	resp, body, err := s.do("GET", s.chunkURL, nil, http.Header{"Range": {"bytes=1000-1999"}})
	if s.checkStatus("ranged GET returns 206", resp, err, 206) {
		s.check("ranged GET returns the asked-for length", len(body), 1000)
		s.check("Content-Range is exact", resp.Header.Get("Content-Range"),
			"bytes 1000-1999/"+strconv.Itoa(cipherSize))
		s.check("ranged bytes match the source", digest(body), digest(s.cipher[1000:2000]))
	}

	// Open-ended range: this is what a resuming downloader actually sends.
	resume := cipherSize - 4096
	_, body, err = s.do("GET", s.chunkURL, nil, http.Header{"Range": {"bytes=" + strconv.Itoa(resume) + "-"}})
	if err == nil {
		s.check("open-ended range returns the rest", len(body), 4096)
	}

	_, body, err = s.do("GET", s.chunkURL, nil, http.Header{"Range": {"bytes=-16"}})
	if err == nil {
		s.check("suffix range returns the last bytes", len(body), 16)
	}

	resp, _, err = s.do("GET", s.chunkURL, nil, http.Header{"Range": {"bytes=999999999-"}})
	s.checkStatus("unsatisfiable range returns 416", resp, err, 416)

	// A multi-range request must come back as the whole entity, not as
	// multipart/byteranges: the real client reads chunk responses over a raw
	// socket and cannot parse MIME parts.
	resp, body, err = s.do("GET", s.chunkURL, nil, http.Header{"Range": {"bytes=0-99, 200-299"}})
	if err == nil {
		s.check("multi-range returns the whole entity", resp.StatusCode, 200)
		s.assert(!strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "multipart"),
			"multi-range is not multipart/byteranges",
			"Content-Type: "+resp.Header.Get("Content-Type"))
		s.check("multi-range body is the whole chunk", len(body), cipherSize)
	}

	resp, _, err = s.do("HEAD", s.chunkURL, nil, nil)
	s.checkStatus("HEAD works", resp, err, 200)
}

func (s *Suite) checkDelete() {
	s.reporter.Section("DELETE /v1/chunks/{id}")

	url := s.BaseURL + "/v1/chunks/" + s.chunkID

	resp, _, err := s.do("DELETE", url, nil, nil)
	s.checkStatus("DELETE without a key is rejected", resp, err, 401)

	resp, _, err = s.do("DELETE", url, nil, http.Header{"Authorization": {"Bearer " + s.APIKey}})
	s.checkStatus("DELETE with the owner key succeeds", resp, err, 204)

	resp, _, err = s.do("GET", s.chunkURL, nil, nil)
	s.checkStatus("the chunk is gone afterwards", resp, err, 404)
}

func (s *Suite) checkMisc() {
	s.reporter.Section("misc")

	resp, _, err := s.do("GET", s.BaseURL+"/v1/chunks/00000000000000000000000000000000", nil, nil)
	s.checkStatus("unknown id is a 404", resp, err, 404)

	resp, _, err = s.do("GET", s.BaseURL+"/v1/chunks/not-a-valid-id", nil, nil)
	s.checkStatus("malformed id is a 404", resp, err, 404)

	oversized := make([]byte, s.maxChunkSize+1024)
	resp, _, err = s.do("POST", s.BaseURL+"/v1/chunks", oversized, http.Header{
		"Authorization": {"Bearer " + s.APIKey},
		"Content-Type":  {"application/octet-stream"},
	})
	s.checkStatus("oversized chunk is rejected", resp, err, 413)

	resp, _, err = s.do("PUT", s.BaseURL+"/v1/chunks", nil, http.Header{
		"Authorization": {"Bearer " + s.APIKey},
	})
	s.checkStatus("unsupported method is a 405", resp, err, 405)
}

// -- internals ---------------------------------------------------------------

func (s *Suite) do(method, url string, body []byte, header http.Header) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}

	return s.send(req)
}

// doChunked sends a body of deliberately unknown length, which is what makes
// the transport omit Content-Length and frame the request as chunked.
func (s *Suite) doChunked(method, url string, body []byte, header http.Header) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, url, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return nil, nil, err
	}
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}

	for name, values := range header {
		req.Header[name] = values
	}

	return s.send(req)
}

func (s *Suite) send(req *http.Request) (*http.Response, []byte, error) {
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return resp, body, nil
}

func (s *Suite) checkStatus(label string, resp *http.Response, err error, want int) bool {
	if err != nil {
		s.fail(label, err.Error())
		return false
	}

	return s.check(label, resp.StatusCode, want)
}

func (s *Suite) check(label string, actual, expected any) bool {
	got, want := fmt.Sprint(actual), fmt.Sprint(expected)
	if got == want {
		s.pass(label)
		return true
	}

	s.fail(label, fmt.Sprintf("expected %q, got %q", want, got))

	return false
}

func (s *Suite) assert(ok bool, label, detail string) {
	if ok {
		s.pass(label)
		return
	}

	s.fail(label, detail)
}

func (s *Suite) pass(label string) {
	s.result.Passed++
	s.reporter.Pass(label)
}

func (s *Suite) fail(label, detail string) {
	s.result.Failed++
	s.reporter.Fail(label, detail)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}
