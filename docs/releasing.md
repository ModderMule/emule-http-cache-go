# Releasing

```sh
./scripts/publish-release.sh          # prompts, defaults to a patch bump
./scripts/publish-release.sh 1.3.0    # explicit
```

That bumps the version, commits, tags, and pushes. The tag push starts three
build workflows, which attach their bundles to a **draft** GitHub Release. Write
the notes on that draft and publish it — nothing goes out until you do.

## Where the version lives

`cmd/version.go` holds one literal, and it is the single source of truth:

```go
var (
	Version = "v0.1.0"
	Commit  = ""
)
```

`scripts/bump-version.sh` rewrites it, `scripts/build.sh` stamps it into the
binary with `-ldflags -X`, and all three workflows scrape it with `sed` to name
their archives. So the tag, the artifact filename and what `emule-http-cache
version` prints cannot drift apart.

The `sed` pattern is anchored to the start of the line in every one of those
places, and it must stay that way — the doc comment above the literal contains a
`cmd.Version=1.0.0` ldflags example, and `commit()` calls `runtime.Version()`.
Both would match an unanchored pattern:

```sh
sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/version.go | head -1
```

`bump-version.sh` reads the literal back after editing it and aborts if it did
not change. That is the one check that catches a pattern which has silently
stopped matching; without it a release would ship binaries still reporting the
previous version and nothing else in the build would notice.

`protocolVersion` in `http_public/info.go` is **not** a release version. It is
the wire contract clients handshake on, asserted by `internal/conformance`, and
it moves only when the contract does.

## The two scripts

`scripts/bump-version.sh` is usable on its own — it stops after tagging locally,
so you can inspect or undo before anything leaves the machine:

```sh
./scripts/bump-version.sh 1.3.0
git show HEAD                       # check the diff
git tag -d v1.3.0 && git reset --hard HEAD~1    # undo
```

It refuses a dirty working tree, a malformed version, and a tag that already
exists. `scripts/publish-release.sh` wraps it, shows what it is about to push,
and on `N` deletes the tag and resets the commit, leaving the repo as it found
it. Neither script dispatches a workflow: the tag push already starts all three,
and dispatching would build every release twice.

## What the workflows produce

`.github/workflows/{linux,macos,windows}.yml`, each on `push: tags: ['v*']` plus
`workflow_dispatch`. They build on native runners, so nothing cross-compiles.

| Workflow | Runner | Archive |
| --- | --- | --- |
| `linux.yml` | `ubuntu-latest` | `emule-http-cache-<version>-linux-amd64.tar.gz` |
| `macos.yml` | `macos-latest` | `emule-http-cache-<version>-macos-arm64.tar.gz` |
| `windows.yml` | `windows-latest` | `emule-http-cache-<version>-win64.tar.gz` |

Each ships a `.sha256` beside its archive. There is no combined `SHA256SUMS`:
three independent workflows cannot append to one file without racing.

The three share a repo-scoped concurrency group, `release-<tag>`, with
`cancel-in-progress: false`. That makes them queue rather than race to create
the same draft release — the first creates it, the other two attach to it.

Windows gets a `.tar.gz` like the others rather than a `.zip`, because the
`bundle` subcommand only emits tar.gz. Windows 10 1803+ ships `bsdtar`, so
`tar xzf` works there out of the box.

Only the tag build attaches to a release. A `workflow_dispatch` run on a branch
still uploads a downloadable artifact, it just has no release to attach it to.

CI uses the built-in `GITHUB_TOKEN` with `permissions: contents: write`. No
personal access token is needed or wanted.

## What is in an archive

`scripts/bundle.sh` drives the `bundle` subcommand, so the layout is flat: the
binary sits at the archive root, which is what `scripts/update.sh` expects when
it untars and runs `./emule-http-cache serve`.

```
emule-http-cache            (or emule-http-cache.exe)
config.example.yaml
README.md
docs/                       incl. nginx.conf.sample
docs_api/                   swagger.json, swagger.yaml
http_public/static/tpl/     the templates, for a --static-file-path deploy
scripts/update.sh
```

`config.yaml` is never included — it holds an upload credential. Nor are `*.go`,
`.git`, `data/`, `testdata/`, or the other build-time scripts.

`update.sh` is the only script that ships, since it is the only one a deployed
copy has any use for. `pkg/bundle` excludes by base name and has no "everything
but" form, so the others are named explicitly in `bundle.sh` — **a newly added
script must be added to that list or it will go out with the next release.**

## Building without releasing

```sh
./scripts/build.sh                  # bin/emule-http-cache for the host
./scripts/bundle.sh                 # dist/…-<host platform>.tar.gz
./scripts/bundle.sh linux           # a specific target
./scripts/bundle.sh macos
./scripts/bundle.sh windows

VERSION=v9.9.9-rc1 ./scripts/build.sh    # override without touching the source
GOOS=linux GOARCH=arm64 ./scripts/build.sh
```

`build.sh` appends `.exe` for `GOOS=windows` and honours `BIN_DIR`. Builds are
`CGO_ENABLED=0`-safe: there is no cgo anywhere in the tree, and the only
syscall dependency is `golang.org/x/sys/unix`. `-trimpath -s -w` keeps the
binary small without disturbing `debug.ReadBuildInfo()`, so the `vcs.revision`
fallback in `commit()` still works.

## Checking a release

```sh
tar tzf emule-http-cache-v1.3.0-linux-amd64.tar.gz | sort
shasum -a 256 -c emule-http-cache-v1.3.0-linux-amd64.tar.gz.sha256

mkdir rel && tar -C rel -xzf emule-http-cache-v1.3.0-linux-amd64.tar.gz
cd rel && ./emule-http-cache version && ./emule-http-cache init && ./emule-http-cache serve
```
