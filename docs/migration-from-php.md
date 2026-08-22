# Migrating from the PHP reference server

The two servers share an on-disk format, so a migration is a config translation
and a restart — not a data conversion.

## The data comes across untouched

The chunk store is byte-identical: `<first two hex of id>/<id>.bin` beside
`<id>.json`, the same sidecar with the same JSON field order and no trailing
newline, the same `quota-<keyId>-<YYYYMMDD>.txt` counters and the same
`gc-last.txt`. Point the Go server at an existing install and every chunk already
there keeps serving, with the same URLs, the same ETags and the same expiry.

```yaml
storage:
  data_dir: /path/to/emule-http-cache-php/storage
  var_dir:  /path/to/emule-http-cache-php/var
```

Run the two side by side for as long as you like, but **do not let both write to
the same `var/` on a platform without `flock`** — the daily quota counters are a
read-modify-write and would drift. On Linux and macOS they interlock correctly.

## Translating config.php

| `config.php` | `config.yaml` |
|---|---|
| `'apiKeys' => ['id' => ['secret' => …]]` | `api_keys: {id: {secret: …}}` |
| `'quotaBytesPerDay'` | `api_keys.<id>.quota_bytes_per_day` |
| `'enabled'` | `api_keys.<id>.enabled` |
| `'openUpload'` | `upload.open_upload` |
| `'openUploadQuotaBytesPerDay'` | `upload.open_upload_quota_bytes_per_day` |
| `'storageDir'` | `storage.data_dir` |
| `'varDir'` | `storage.var_dir` |
| `'maxChunkSize'` | `storage.max_chunk_size` |
| `'minFreeBytes'` | `storage.min_free_bytes` |
| `'defaultTtl' => 172800` | `storage.default_ttl: 48h` |
| `'maxTtl' => 604800` | `storage.max_ttl: 168h` |
| `'publicBaseUrl'` | `server.public_base_url` |
| `'gcProbability'` | — see below |

TTLs become durations (`48h`) rather than second counts. Every key also has an
environment override: `storage.max_chunk_size` is `STORAGE_MAX_CHUNK_SIZE`.

A config carrying `gcProbability` still loads — a migration must never be blocked
by a key we stopped honouring — and the server warns once at startup that it is
ignored.

## What changed, and why

**Expiry is a timer, not a dice roll.** PHP had no long-lived process, so it
swept on a daily stamp plus a 1% chance per upload, and the README had to say
that cron was the only real guarantee. Here a goroutine sweeps on `gc.interval`
(hourly by default) and a quiet server still reclaims. To keep the cron model,
set `gc.interval: 0` and run `emule-http-cache gc` on a schedule; drop the
`bin/gc.php` crontab entry either way.

**Uploads no longer trigger cleanup.** Nothing is lost: the timer does not need
traffic to fire.

**The config file is mode 0600.** PHP needed 0644 because `bin/gc.php` and the
test suite ran as the shell user while Apache owned the file. One daemon has one
identity, and the file holds an upload credential.

**`.htaccess` and the FastCGI wiring are gone.** There is no webroot to protect:
`storage/` is never web-reachable by construction, and the `Authorization`
header arrives intact without a rewrite rule. `docs/nginx.conf.sample` is now a
reverse-proxy sample rather than a php-fpm one.

**Nothing changed on the wire.** Same routes, same status codes, same error
shape, same headers. The PHP conformance suite passes against this server
unmodified, which is the check to run if you doubt it:

```sh
php /path/to/emule-http-cache-php/tests/smoke.php http://localhost:8080 <key>
```

## Keys and links

Existing API keys keep working — copy the secrets across verbatim. Every
`ed2k://|httpcache|` link already handed out keeps working too, since the format
and the base URL are unchanged.

`/install` will not read a hand-written config back to anyone: disclosure is
gated on `var/install.json`, which only the installer writes. A migrated config
has no marker, so that page says so and shows nothing.
