# Architecture

How this server is put together, and why the parts that look odd are that way.

## The shape of the thing

```
main.go               three lines of body, plus the swagger general-info block
cmd/                  cobra commands: serve, init, gc, conformance, bundle, version
log/                  zap + lumberjack behind a small Logger interface
internal/config       viper loading, defaulting and validation
internal/security     API-key authentication
internal/install      writes config.yaml and the install marker
internal/conformance  the portable contract test
pkg/storage           the filesystem blob store, quota accounting and the expiry sweep
pkg/ed2k              the ed2k://|httpcache| configuration link
pkg/baseurl           one definition of "a cache base URL"
pkg/bundle            the deployable tar.gz builder
http_public/          the gin server, its handlers and its HTML pages
docs_api/             the generated OpenAPI spec
```

Dependency direction is one way: `config` is the leaf everything else imports,
`http_public` sits on top, and nothing below `cmd/` imports it.

`pkg/` holds what a third-party Go program could reasonably want on its own:
`ed2k` and `baseurl` are pure and have no dependency outside the standard
library, and `bundle` is self-contained. `storage` is the exception and only
half-portable — its `NewStore`, `NewQuota` and `NewGc` constructors take a
`*config.Config`, which lives under `internal/` and so cannot be named from
outside this module. Everything else on `Store` is reachable, but an external
caller cannot build one. Closing that gap means giving `storage` a small
options struct of its own (it reads nine config fields) and letting `cmd/`
translate; until then, treat `pkg/storage` as public in layout and internal in
practice.

## Why the client constrains the server so tightly

eMuleQt does not fetch chunks with an HTTP library. `HttpCacheClient` builds the
request by hand and reads the response over a raw `EMSocket`, because the
transfer has to be interleaved with the rest of the eD2K stack. Four consequences
shape almost every decision in `http_public`:

**There is no chunked decoder.** Everything after the blank line is treated as
payload and fed to SHA-256 and AES-CBC. If a response ever arrived with
`Transfer-Encoding: chunked`, the hex length lines would become "ciphertext", the
digest would fail, and the client would report `Corrupt` — not an HTTP error. So
every chunk response sets `Content-Length` explicitly. Left unset, `net/http`
switches to chunked as soon as the body outgrows its 2 KB buffer, which a
9.28 MB chunk does immediately. `http_public/wire_test.go` dials a socket and
asserts the bytes on the wire, because an `httptest` round trip decodes chunked
transparently and would show a perfectly correct body either way.

**Response headers are capped at 2048 bytes**, counted as the sum of trimmed line
lengths with the status line included, and a single line over 1024 bytes is
*silently truncated* rather than rejected — which would corrupt a `Content-Range`
instead of failing loudly. Our header set measures 326 bytes. That is a lot of
headroom, but it is not infinite: Cloudflare's `Report-To`, `NEL` and
`__cf_bm` cookie together routinely add 700–950 bytes, and the failure at the cap
is a bare TCP disconnect that looks exactly like a dead server.

**A 3xx is fatal.** The client does not follow redirects on the chunk path, so
gin's `RedirectTrailingSlash` and `RedirectFixedPath` are both turned off in
`Handler()`. Without that, `/v1/chunks/<id>/` would answer 301 and the fetch
would be abandoned rather than 404ing honestly.

**Multipart cannot be parsed.** A multi-range request is answered with the whole
entity and a 200, which RFC 9110 §14.2 explicitly permits. This is why the range
logic is a port of the PHP server's `ByteRange` rather than a call to
`http.ServeContent`, which would send `multipart/byteranges`.

## Timeouts

`ReadTimeout` and `WriteTimeout` are absolute deadlines measured from the start
of a request, so any value large enough to let 9.28 MB finish on a slow link
stops working as a stall detector. Both default to zero and the transfer paths
roll a deadline per slice instead — 512 KiB out, 1 MiB in — which is what
distinguishes "slow but progressing" from "stalled".

`shutdown_timeout` defaults to 120 s for the same reason: `http.Server.Shutdown`
does not interrupt a handler that is already running, and a full part at a real
100 KB/s takes 97 seconds. A conventional 10-second grace would guillotine
legitimate uploads.

## Why the sidecar is written before the blob is renamed

`Store.Ingest` writes `<id>.json` and only then renames `.tmp-<id>` into place.
The order is load-bearing, and not for the obvious reason.

A reader that sees only one of the two is fine either way: `Meta` reads the
sidecar and then checks the blob exists, so both halves report the chunk as
absent. The asymmetry is in the sweep. `AllIDs` enumerates `*.json` and nothing
else, so **a blob with no sidecar is invisible to every future sweep** — 9.28 MB
that can never be reclaimed. A sidecar with no blob is enumerated, read, and
reaped at its expiry.

So sidecar-first leaks a self-reclaiming 200-byte file where blob-first would
leak a permanent 9.28 MB one.

## Why an https base URL is refused

`URLClient::setUrl` takes `parsed.port(80)` and opens a bare socket. There is no
TLS anywhere on the chunk-download path. An `https://` chunk URL therefore
connects to port 443 and speaks cleartext at it, and every fetch fails — while
uploads, which go through Qt's network stack, keep working. The symptom is
"publishing succeeds, no peer can fetch", which is a miserable thing to debug.

So the server never *derives* an https base URL, even behind TLS, and warns
loudly at startup if one is pinned. Terminate TLS in front and pin the http
address the proxy forwards to.

## Locking

The per-key daily counter is a read-modify-write on a small file. It is guarded
twice: a `sync.Mutex` keyed by counter path on the outside, so at most one
goroutine per counter parks an OS thread, and `flock` on the inside for
cross-process safety with the `gc` subcommand or a co-resident PHP install.

`flock(2)` locks the open file description rather than the process, so two
goroutines that each opened the path genuinely contend — the opposite of POSIX
`fcntl` record locks, which are per-process and would be silently useless inside
one daemon. On platforms without `flock` the in-process mutex is the only guard,
and `serve` says so once at startup.

## Hot reload after a browser install

A server started with no config answers `/install` and 503s every `/v1` route.
When the install page writes a config it calls back into `cmd/serve.go`, which
re-parses it, builds a new store and quota, restarts the sweeper, and hands the
result back; `http_public` swaps its whole state atomically.

The PHP server needs none of this because it re-reads its config on every
request. A daemon that loaded once at boot would otherwise finish a setup flow by
telling the operator everything worked while still refusing every upload.
