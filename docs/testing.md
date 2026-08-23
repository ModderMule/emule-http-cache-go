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

## The download handler

The contract test proves a chunk round-trips; it says almost nothing about the
headers wrapped around it. `http_public/download_test.go` covers what
`serveChunk` emits:

- the six headers set before the Range branch — `Content-Type`, `Accept-Ranges`,
  `ETag`, `Cache-Control`, `X-Chunk-Expires`, `X-Content-Type-Options` —
  asserted on the 200, 206 **and** 416 paths alike, since a 416 is still a
  response about a real chunk and must carry the same validator;
- the ETag compared against a digest computed from the payload the test itself
  supplied, so it proves the header is derived from the bytes rather than merely
  agreeing with the sidecar;
- `Content-Range: bytes */<size>` on every unsatisfiable range, which is how a
  client learns the real size after guessing wrong;
- HEAD carrying a `Content-Length` with no body, plain and ranged;
- the `If-None-Match` matrix, including the deliberate choice **not** to answer
  304 to a Range request — a resuming downloader already knows the entity is
  unchanged, it wants the bytes it is missing;
- an expired chunk answering 404 without being deleted, since a GET must never
  do write work;
- a blob truncated under a live server never being served as a complete body.
  That last one matters more than it looks: a silently short body decrypts to
  garbage and is reported as `Corrupt`, and three of those retire a healthy
  cache entry. A dropped connection is recoverable; a plausible truncation is
  not.

Two of these assertions cannot fail against any correct Go server, because
`net/http` enforces them itself — it supplies `Content-Length: 0` when a handler
writes no body, and it discards a body written under HEAD. They are kept because
they pin the wire contract a non-Go implementation also has to meet; the
`isHead` checks in `download.go` are there to avoid reading 9.7 MB off disk, not
to keep the body off the wire.

The PHP server has no counterpart to any of this. `tests/unit.php` runs only
`StorageTest` and `InstallTest`, so its `ByteRange` and `RangeResponse` have no
unit coverage at all — only the live-server `SmokeTest`.

## Storage compatibility

`pkg/storage.TestSidecarIsByteIdenticalToPHP` decodes a sidecar written by
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
