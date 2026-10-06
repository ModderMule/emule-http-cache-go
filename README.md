# eMule HTTP Cache

A small, self-contained chunk cache for eMuleQt's **HTTP Cache** feature.

An uploader that sees several peers wanting the same 9,728,000-byte part encrypts
it with a fresh AES-256-CBC key, `POST`s the ciphertext here once, and hands the
URL and key to each peer over the eD2K link. One upload serves N downloaders, and
**the server never sees anything useful** — no key, no IV, no eD2K file hash, no
part number, no filename.

This is a Go port of the PHP reference server, sharing its on-disk format and its
wire contract exactly. `internal/conformance` is the conformance suite for any
implementation, and it passes in both directions against the PHP original.

---

## Install

Grab the archive for your platform from the [latest release][releases], unpack
it, and you have everything you need beside the binary:

```sh
tar xzf emule-http-cache-<version>-linux-amd64.tar.gz
./emule-http-cache init          # writes config.yaml, prints the key once
./emule-http-cache serve
```

Instead of `init`, you can skip straight to `serve` and open `http://<ip>:8080/install` in a browser.

Builds are published for `linux-amd64`, `macos-arm64` and `win64`. Each carries a
`.sha256` next to it:

```sh
shasum -a 256 -c emule-http-cache-<version>-linux-amd64.tar.gz.sha256
```

Or let the updater do all of that — it picks the archive for your platform,
verifies the checksum, unpacks it into the current directory and starts the
server:

```sh
curl -fLO https://github.com/ModderMule/emule-http-cache-go/releases/latest/download/update.sh
bash update.sh
```

### Updating

Every archive carries the same script as `scripts/update.sh`. Run it from an
install to move to the latest published release; `config.yaml` and `data/` are
left alone.

```sh
./scripts/update.sh                          # install the latest release, then serve
./scripts/update.sh --config /etc/ehc.yaml   # arguments go to `serve`
UPDATE_VERSION=v0.1.3 ./scripts/update.sh    # a specific tag
UPDATE_NO_START=1 ./scripts/update.sh        # install only, e.g. under systemd
```

[releases]: https://github.com/ModderMule/emule-http-cache-go/releases

## Quick start

Building it yourself instead:

```sh
./scripts/build.sh
./bin/emule-http-cache init          # writes config.yaml, prints the key once
./bin/emule-http-cache serve
```

`init` prints an `ed2k://|httpcache|…` link that configures eMuleQt in one step: copy it, and
eMuleQt's clipboard watcher offers to apply it. The format is in
[`docs/ed2k-httpcache-link.md`](docs/ed2k-httpcache-link.md).

That link needs an address other machines can reach, and `init` has no request to read one
from. So it asks a public echo service (ipify, icanhazip, Cloudflare) for this machine's
address, falls back to the network interfaces when none answers, and pins the result as
`server.public_base_url`. It says which it was: a confirmed public address, a public address
behind a router that still needs a port forward, or a private one that only works on the LAN.
Pass `--public-base-url http://cache.example.com` to name the address yourself — `init` then
makes no network request at all.

Prefer a browser? Start the server without a config and open `/install` — it does
the same thing, and answers `503` on every `/v1` route until it has been through.
Open it by the address clients will use: the link is built from the address in your browser,
and the page warns you if that is `localhost`.

Interactive API docs are at `/swagger/index.html`.

## Requirements

Go 1.26+ to build (see `go.mod`). Nothing at runtime: one static binary, and a
directory it can write to.

---

## The contract

Base URL is wherever the server is reachable. All error bodies are
`{"error": "<message>", "status": <code>}`.

`/install`, `/` and `/swagger` are not part of this. They are this
implementation's own pages; another backend reimplements `/v1/*` and nothing
more.

### `GET /v1/info`

No auth. Lets a client discover limits before it wastes an upload.

```json
{
  "service": "emule-http-cache",
  "version": 1,
  "implementation": "go",
  "maxChunkSize": 10485760,
  "defaultTtl": 172800,
  "maxTtl": 604800,
  "rangeSupported": true,
  "uploadRequiresAuth": true
}
```

`service` and `version` are the handshake: a client that does not see
`"service":"emule-http-cache"` must refuse to use the endpoint.

`uploadRequiresAuth` is `false` on a server that takes uploads without a
credential. It is never a reason for a client to drop the key it has — a key is
still what authorises `DELETE`, and the operator may close the server tomorrow.

### `POST /v1/chunks`

Stores one encrypted chunk.

| | |
|---|---|
| `Authorization` | `Bearer <apiKey>` — `X-Api-Key: <apiKey>` also accepted. Optional when the server has `open_upload` on |
| `Content-Type` | `application/octet-stream` |
| `Content-Length` | **required** — chunked uploads are rejected with `411` |
| `X-Chunk-TTL` | optional, seconds; clamped to `maxTtl` |
| body | raw ciphertext |

**201**

```json
{
  "id": "87d7f7573b0263fc9faf9ed65cb62841",
  "url": "http://localhost:8080/v1/chunks/87d7f7573b0263fc9faf9ed65cb62841",
  "size": 9728016,
  "sha256": "…",
  "expires": 1755500000
}
```

`url` is absolute and is the only field a client may fetch from — never rebuild
it from `id`, since a backend may serve blobs from another host. `Location`
carries the same value. `expires` is an absolute unix timestamp, not a duration.

Errors: `400` empty body or length mismatch · `401` bad key, or a missing one on
a server that requires it · `411` no `Content-Length` · `413` over
`maxChunkSize` · `429` daily quota exhausted · `507` storage failure, or free
space that would drop below `min_free_bytes`. `429` and `507` carry a
`Retry-After` in delta-seconds.

### `GET /v1/chunks/{id}`

No auth. Supports `Range`. `HEAD` behaves identically without a body.

Responds `200` (full) or `206` (ranged) with `Content-Type:
application/octet-stream`, `Accept-Ranges: bytes`, `ETag`, `Cache-Control:
public, max-age=<remaining ttl>, immutable` and `X-Chunk-Expires: <unix>`.
Unknown or expired id → `404`. Unsatisfiable range → `416` with `Content-Range:
bytes */<size>`.

Only single ranges are honoured; a multi-range request gets the whole entity,
which RFC 9110 §14.2 permits and which is what the client can actually parse.
**Range support is mandatory for a conforming backend**: a downloader that drops
mid-chunk resumes with `Range: bytes=<offset>-`, using the preceding ciphertext
block as the CBC IV, and without `206` would have to restart a 9.28 MB transfer.

### `DELETE /v1/chunks/{id}`

`Authorization: Bearer <apiKey>`, and only the uploader's key works. `204` on
success. A chunk belonging to another key reports `404`, not `403`, so a valid
key cannot probe the id space.

Errors: `401` bad/missing key · `404` unknown, expired, or already deleted ·
`500` the chunk could not be removed and is still on disk.

eMuleQt does **not** call this automatically: a failed download is as likely to
be the downloader's or the network's fault as the blob's, so entries lapse at
their TTL instead. It exists for explicit cleanup.

### `GET /v1/stats`

How full and how busy the server is, for whoever runs it. **An extension of this
implementation, not part of the contract**: no client calls it, the PHP
reference server answers `404`, and a conforming backend may do the same.

`Authorization: Bearer <apiKey>` with any enabled key. A caller on the server
itself needs none — a loopback connection carrying no `X-Forwarded-For`,
`X-Real-IP` or `Forwarded` header — so `curl localhost:8080/v1/stats` works from
a shell while a request relayed by nginx always needs the key.

```json
{
  "startedAt": 1791283200,
  "storage": {
    "chunks": 1204,
    "bytes": 11712531264,
    "expiredChunks": 37,
    "expiredBytes": 359936592,
    "asOf": 1791369600
  },
  "traffic": {
    "uploads":         { "total": 5210,        "last24h": 310 },
    "uploadedBytes":   { "total": 50683000000, "last24h": 3015000000 },
    "downloads":       { "total": 18422,       "last24h": 1207 },
    "downloadedBytes": { "total": 91200000000, "last24h": 6100000000 },
    "deletes":         { "total": 96,          "last24h": 4 },
    "gcExpired":       { "total": 4010,        "last24h": 288 },
    "rejected": {
      "unauthorized":  { "total": 12,  "last24h": 0 },
      "notFound":      { "total": 733, "last24h": 41 },
      "tooLarge":      { "total": 1,   "last24h": 0 },
      "quotaExceeded": { "total": 9,   "last24h": 9 },
      "storageFull":   { "total": 0,   "last24h": 0 }
    }
  },
  "clients": { "total": 842, "last24h": 97, "approximate": true }
}
```

- **`storage`** is read off the disk and reused for five seconds; `asOf` is when.
  `chunks` and `bytes` cover everything present, including chunks past their TTL
  that the next sweep will remove, which `expiredChunks` and `expiredBytes`
  break out.
- **`traffic`** is counted in memory. `total` runs from `startedAt` and a restart
  zeroes it; `last24h` is kept in hourly buckets, so it covers 24 to 25 hours.
  A download is a `GET` that returned chunk bytes, whole or a `Range`, and
  `downloadedBytes` is what was actually sent. `gcExpired` sees this process's
  own sweeper, not a `gc` run from cron. `rejected` counts `/v1` responses by
  status: `401`, `404`, `413`, `429`, `507`.
- **`clients`** is the number of distinct addresses that called a `/v1` route
  other than this one. It is an estimate: the server keeps no addresses and
  counts them with HyperLogLog, exact for a handful and within about 1 % beyond.
  Behind a reverse proxy on the same host the address is taken from `X-Real-IP`.
  See `docs/architecture.md`.

Errors: `401` bad/missing key.

---

## API keys

`config.yaml` holds as many as you like, each with its own daily allowance:

```yaml
api_keys:
  laptop:
    secret: "…"
    quota_bytes_per_day: 5368709120
  seedbox:
    secret: "…"
    quota_bytes_per_day: 0        # unlimited
  old-box:
    secret: "…"
    enabled: false                # revoked, but still on the books
```

The id labels the chunk's owner and its counter under `data/var/`, so keep it to
`[A-Za-z0-9._-]`.

`enabled: false` revokes one uploader without deleting the entry: every client
and every `ed2k://` link carrying that secret stops working at once, `DELETE`
included, and you still know whose it was.

### Letting anyone upload

`upload.open_upload: true` accepts `POST /v1/chunks` with no credential at all.
Two consequences, neither obvious:

- An anonymous upload is owned by the reserved key id `anonymous`, which nobody
  can authenticate as. Those chunks **cannot be deleted through the API** and only
  lapse at their TTL. A key named `anonymous` in the config is ignored on load,
  precisely so that ability cannot be handed out.
- A *wrong* key is still a `401`. Only an absent one falls through to anonymous,
  so a client with a mistyped secret finds out instead of quietly uploading
  chunks it can never delete.

Set `upload.open_upload_quota_bytes_per_day` — the entire internet shares that
one counter — and set `storage.min_free_bytes` as well.

## Why the download URL has no auth

The `id` is 128 bits from a CSPRNG and it *is* the capability: guessing one is
infeasible, and the uploader hands it only to peers it chose. Requiring a key on
the `GET` would mean sharing the uploader's credential with every downloader,
which is strictly worse. The real protection is that the body is AES-256-CBC
ciphertext, keyed per chunk and shared only over the eD2K link — this server's
disks and backups hold nothing but opaque blobs of a uniform size.

Consequences an operator should know:

- **Do not log request bodies**, and prefer not to log full URLs — a URL is a
  bearer token. This server's own access log replaces chunk ids with `<id>`; a
  proxy in front needs telling separately, see `docs/nginx.conf.sample`.
- Chunks are immutable and short-lived. There is no update verb.
- Uniform 9,728,016-byte blobs are the norm; anything else is a short tail part
  or an abuse attempt.
- `quota_bytes_per_day` is fairness — it caps one key for one UTC day, so N keys
  can still fill the volume between them. `min_free_bytes` is host protection:
  uploads are refused with `507` once free space would drop below it, whoever is
  asking. Set both.

## Keep the chunk URL on http

eMuleQt downloads chunks over a hand-built socket with no TLS, dialling port 80.
An `https://` chunk URL connects to 443 and speaks cleartext at it, so every
fetch fails while uploads keep working. Terminate TLS in front if you want it for
the browser pages, and pin the http address peers should use:

```yaml
server:
  public_base_url: "http://cache.example.com"
```

The server refuses to derive an https base URL for this reason, and warns at
startup if you pin one.

## Storage layout

```
data/storage/<first 2 hex of id>/<id>.bin     ciphertext
data/storage/<first 2 hex of id>/<id>.json    {id,size,sha256,ownerKeyId,createdAt,expiresAt}
data/var/quota-<keyId>-<YYYYMMDD>.txt         bytes charged today
data/var/gc-last.txt                          unix time the last sweep started
data/var/install.json                         the install marker; holds no secret
```

Identical to the PHP reference server's, so this binary can be pointed at an
existing install — see `docs/migration-from-php.md`. Uploads land in `.tmp-<id>`
and are renamed into place, so a reader can never observe a partial chunk.

## Expiry

Reads never delete — a `GET` must not do write work. A goroutine sweeps every
`gc.interval` (hourly by default), reclaiming expired chunks, interrupted uploads
older than an hour, and quota counters older than a week.

Prefer cron? Set `gc.interval: 0` and schedule the subcommand:

```cron
17 * * * * /path/to/emule-http-cache gc >/dev/null 2>&1
```

## Commands

```
emule-http-cache serve                     start the server
emule-http-cache init                      write a config and print the key once
emule-http-cache gc [maxDeletes]           reclaim expired chunks and exit
emule-http-cache conformance URL [KEY]     check any backend against the contract
emule-http-cache bundle                    tar.gz the binary with its static files
emule-http-cache version
```

A `--config` flag, `$EMULE_HTTP_CACHE_CONFIG`, or `./config.yaml`, in that order.
Every setting also takes an environment override: `server.addr` is `SERVER_ADDR`.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — how it fits together, and why the client constrains it so tightly
- [`docs/testing.md`](docs/testing.md) — the conformance suite and the raw-wire tests
- [`docs/releasing.md`](docs/releasing.md) — cutting a release, and what the build workflows publish
- [`docs/migration-from-php.md`](docs/migration-from-php.md) — moving an existing install across
- [`docs/ed2k-httpcache-link.md`](docs/ed2k-httpcache-link.md) — the configuration link format
- [`docs/nginx.conf.sample`](docs/nginx.conf.sample) — putting a reverse proxy in front
