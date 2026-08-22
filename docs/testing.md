# Testing

```sh
./scripts/test.sh          # gofmt, go vet, go test ./...
go test ./... -short       # skips the two suites that move a 9.28 MB part
```

Every test logs its inputs and outputs with `t.Logf`, per the project's Go
standards, so `go test -v` reads as a transcript of what was tried.

## The contract test

`internal/conformance` is the portable one: 31 assertions over the whole REST
surface, speaking nothing but HTTP. It is a library with no `testing` import so
it can be driven two ways.

```sh
go test ./internal/conformance                       # in-process, over a temp dir
go test ./internal/conformance -base URL -key KEY    # against any backend
./bin/emule-http-cache conformance URL KEY           # same suite, no Go toolchain
```

With no `-base` it installs a throwaway server and runs against it, so a plain
`go test ./...` covers install, serve and the contract together.

**Both directions must pass.** Running the Go suite against the PHP reference
server proves this is a port of the contract rather than a description of this
implementation; running the PHP suite against this server proves the reverse:

```sh
./bin/emule-http-cache serve &
php /path/to/emule-http-cache-php/tests/smoke.php http://localhost:8080 <key>

go test ./internal/conformance -base http://localhost/emule-http-cache-php -key <key>
```

The suite reads `uploadRequiresAuth` from `/v1/info` and asserts what that server
actually promises, so an open server passes the same 31.

Its `http.RoundTripper` reports every exchange to the reporter — method, URL,
headers, body length and digest, never bodies — and the test reporter is `t`. So
the logging requirement is met once, structurally, rather than thirty-one times
by hand. Credentials are redacted and chunk ids are never expanded.

## What the contract test cannot reach

`http_public/wire_test.go` dials a TCP socket and writes exactly what
`URLClient::buildGetHeader` writes, then reads the raw response. This is the only
way to catch the chunked-encoding trap: `net/http`'s client decodes chunked
transparently and would report a perfectly correct body from a server that had
silently started framing it that way. It asserts the literal absence of
`Transfer-Encoding`, an exact `Content-Range`, and that the headers fit inside
the client's own 2048-byte accounting.

`TestNoRedirectOnChunkPath` pins gin's two redirect defaults off, since a 3xx on
that path is a fatal fetch error for the real client.

## Storage compatibility

`internal/storage.TestSidecarIsByteIdenticalToPHP` decodes a sidecar written by
the PHP server, re-encodes it, and asserts byte equality. That is the cheapest
possible proof that a Go server writing into a store a PHP server also reads
stays readable by it — the JSON field order in `ChunkMeta` is a wire format, not
a style choice.

To exercise the drop-in path by hand, point the server at an existing PHP
install and turn the sweeper off so it cannot reclaim that install's chunks:

```sh
GC_INTERVAL=0 \
STORAGE_DATA_DIR=/path/to/emule-http-cache-php/storage \
STORAGE_VAR_DIR=/path/to/emule-http-cache-php/var \
./bin/emule-http-cache serve
```

## Against the real client

eMuleQt's own live test drives a real publisher and downloader — publish, GET,
decrypt, byte-exact compare, plus a ranged resume:

```sh
EMULE_HTTPCACHE_URL=http://localhost:8080 EMULE_HTTPCACHE_KEY=<key> tst_HttpCacheLive
```
