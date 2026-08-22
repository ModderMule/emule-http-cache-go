# `ed2k://|httpcache|` — the HTTP Cache upload config link

A one-click way to hand eMuleQt an HTTP Cache endpoint and the API key for it, in the link format
eMule users already paste around.

The server's install page prints one of these. A user clicks it, eMuleQt asks "configure the HTTP
Cache to use `cache.example.com`?", and that is the whole setup. No copying a 48-character secret out
of a browser and into a settings dialog.

**The link contains an upload credential.** Everything in the [Security](#security) section is
normative, not advice.

---

## At a glance

```
ed2k://|httpcache|HTTP%20Cache%20upload%20config|https://cache.example.com|1f4b9c02d7e35a68|/
```

with the optional key id:

```
ed2k://|httpcache|HTTP%20Cache%20upload%20config|http://192.168.1.10/emule-http-cache-php|1f4b9c02d7e35a68|k=default|/
```

Three positional fields — name, base URL, secret — then optional `key=value` fields. The same shape
eMule already uses for `ed2k://|file|name|size|hash|p=<hashset>|s=<url>|/`, so a parser written for
one reads like a parser written for the other.

---

## Grammar

```abnf
link        = "ed2k://|" type "|" name "|" base-url "|" secret *( "|" option ) "|/"

type        = "httpcache"          ; ASCII case-insensitive
name        = field                ; may be empty
base-url    = field                ; absolute http/https, see below
secret      = field                ; never empty
option      = opt-name "=" field
opt-name    = ALPHA *( ALPHA / DIGIT )

field       = *( unreserved-octet / pct-encoded )
unreserved-octet = %x21-24 / %x26-7B / %x7D-7E   ; printable ASCII except "%" and "|"
pct-encoded = "%" HEXDIG HEXDIG
```

`ed2k://` and `httpcache` are matched case-insensitively. Everything else is case-sensitive: a
secret is opaque and a base URL's path may not be.

### Fields

| # | Field | Required | Meaning |
|---|---|---|---|
| 1 | `name` | yes, may be empty | Display label, for a confirmation dialog and a settings list. Never an identifier — two links may share a name. When empty, show the base URL's host instead. |
| 2 | `baseUrl` | yes | Absolute `http://` or `https://` URL of the cache root — the thing `/v1/info` hangs off. No trailing slash, no query, no fragment, no `user:pass@`. |
| 3 | `secret` | yes | The API key. Opaque; never parse it. Printable ASCII, no whitespace, at most 512 characters. |

### Options

| Option | Meaning |
|---|---|
| `k=<keyId>` | Which credential this is, `[A-Za-z0-9._-]{1,32}`. Display only — the client never sends it anywhere. It exists so a user with three caches can tell the entries apart. |

Any other `key=value` is **ignored**, not rejected. That is the extension point.

---

## Encoding

`|` is the field separator, so it is the one octet a field may never carry literally.

**A parser splits on `|` first and percent-decodes each field afterwards.** In that order, always.
Decoding first would let a `%7C` inside a name split the link into the wrong number of fields, which
is the classic way a link format grows an injection bug.

A producer therefore percent-encodes, per field:

- `|` (`%7C`) and `%` (`%25`) — the two syntax octets
- every octet outside `%x21`–`%x7E` — space becomes `%20`, and non-ASCII is encoded as its UTF-8
  octets, so `ü` is `%C3%BC`

`:` and `/` are left literal, which keeps `https://cache.example.com/emule` readable in the link.
Encoding them is legal and a parser must accept it; producing them encoded is merely unfriendly.

A `%` that is not followed by two hex digits makes the whole link invalid. Do not fall back to
treating it literally.

---

## Consuming a link

A client that follows these steps in this order cannot be talked into anything by a hostile link.

1. **Parse and validate** against the grammar. On any failure, stop — show "that is not a valid HTTP
   Cache link" and nothing else. Never partially apply one.
2. **Handshake before you store anything.** `GET <baseUrl>/v1/info` and require
   `"service": "emule-http-cache"` with a `version` you understand. Anything else — a 404, an HTML
   page, a different `service` — means the URL is not a cache, and the link is refused. This is what
   stops a link being used to point a client at an arbitrary host.
3. **Ask the user**, showing the `name`, the **host**, and whether the scheme is `https`. Never apply
   a link silently, not even one the user just clicked. Over plain `http://` say so plainly: the key
   and every chunk URL cross the network in the clear, and a chunk URL is a bearer token.
4. **Store** `baseUrl` → `httpCache.baseUrl`, `secret` → `httpCache.apiKey`, and set
   `httpCache.enabled: true`. Keep `k=` alongside as a label if you show one.

`/v1/info` may report `"uploadRequiresAuth": false`, meaning that server takes uploads with no
credential. **This is not a reason to drop the key.** It is still what authorises `DELETE`, and the
operator may close the server tomorrow without reissuing a single link.

### Rejection rules

Refuse the link outright — no dialog, no partial import — when:

- the type token is not `httpcache`
- it does not begin `ed2k://|` or does not end `|/`
- there are fewer than three fields
- an option after the third field has no `=`
- any field holds a `%` that does not start a valid escape
- `baseUrl` is relative, is not `http`/`https`, has no host, or carries `user:pass@`, a query or a
  fragment
- `secret` is empty, holds whitespace, or exceeds 512 characters
- `k=` is present and is not `[A-Za-z0-9._-]{1,32}`
- the whole link exceeds 4096 octets

Refusing a malformed tail field rather than skipping it is deliberate: silently ignoring `k-default`
would let a typo swallow a real option, and the user would never learn why their key id vanished.

---

## Security

- **The link is the credential.** Anyone holding it can upload to that server under that key and
  delete anything it uploaded. Treat it exactly as you would treat the raw API key.
- **Never log it**, not at debug level, not in a crash report, not in a "recently opened links"
  list that syncs anywhere.
- **Never put it in a page a crawler reaches**, a forum post, a channel topic, or a screenshot.
  Unlike a file link, it is not meant to be shared: it configures *your* client, once.
- **Confirm before applying.** A link can arrive from a web page, a chat message, or a `.emulecollection`
  file. Step 2's handshake proves the endpoint is a cache; step 3's dialog is what proves the *user*
  meant to use it.
- **Prefer `https`.** Over `http` the key is on the wire in every request, and so is every chunk URL,
  which is the only thing protecting that chunk's ciphertext from being fetched by a stranger.
- To revoke a link, the operator sets `enabled: false` on that key in `config.yaml`. Every copy of
  the link dies at once, and the entry stays in place so they still know whose it was.

---

## Versioning

Extend only by adding optional `key=value` fields. Old clients skip what they do not know, and that
is the whole compatibility story.

A breaking change takes a **new type token** — `httpcache2` — and never a `v=2` field. An old parser
would ignore `v=2` and then confidently misread whatever followed, which is worse than not
understanding the link at all.

---

## Reference implementation and test vectors

`internal/ed2k/link.go` in this repository builds and parses these links, and
`internal/ed2k/link_test.go` round-trips the vectors below and refuses the ones further down — the
tables in that file are lifted from this page, so the document *is* the test. It is a reference, not
a dependency: the format is small enough to implement from this page alone.

| Name | Base URL | Secret | `k=` | Link |
|---|---|---|---|---|
| `HTTP Cache upload config` | `https://cache.example.com` | `1f4b9c02d7e35a68` | — | `ed2k://\|httpcache\|HTTP%20Cache%20upload%20config\|https://cache.example.com\|1f4b9c02d7e35a68\|/` |
| `HTTP Cache upload config` | `http://192.168.1.10/emule-http-cache-php` | `1f4b9c02d7e35a68` | `default` | `ed2k://\|httpcache\|HTTP%20Cache%20upload%20config\|http://192.168.1.10/emule-http-cache-php\|1f4b9c02d7e35a68\|k=default\|/` |
| `Nachbars WLAN \| Cache` | `https://cache.example.com` | `abc123` | `seedbox` | `ed2k://\|httpcache\|Nachbars%20WLAN%20%7C%20Cache\|https://cache.example.com\|abc123\|k=seedbox\|/` |
| `Zwischenspeicher für eMule` | `https://cache.example.com` | `abc123` | — | `ed2k://\|httpcache\|Zwischenspeicher%20f%C3%BCr%20eMule\|https://cache.example.com\|abc123\|/` |

The third and fourth are the ones worth testing against: a literal `|` in the name that must survive
as `%7C`, and a non-ASCII name that must be encoded as UTF-8 octets rather than as anything else.

Links that must be **refused**:

```
ed2k://|file|x|1|abc|/                          wrong type
ed2k://|httpcache|name|https://h|/              only two fields
ed2k://|httpcache|n|https://h|s                 no |/ terminator
ed2k://|httpcache|n|ftp://h|s|/                 not http(s)
ed2k://|httpcache|n|/relative|s|/               not absolute
ed2k://|httpcache|n|https://u:p@h|s|/           carries credentials
ed2k://|httpcache|n|https://h?q=1|s|/           carries a query
ed2k://|httpcache|n|https://h|s|junk|/          tail field without "="
ed2k://|httpcache|n|https://h||/                empty secret
ed2k://|httpcache|n|https://h|s|k=has spaces|/  malformed key id
ed2k://|httpcache|n|https://h|s%ZZ|/            broken percent escape
```

And two that must be **accepted**:

```
ed2k://|httpcache|n|https://h|s|x=1|k=abc|/     unknown option x= is skipped, k= is read
ED2K://|HTTPCACHE|n|https://h|s|/               scheme and type are case-insensitive
```
