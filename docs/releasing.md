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

Windows gets a `.tar.gz` like the others rather than a `.zip`, because the
`bundle` subcommand only emits tar.gz. Windows 10 1803+ ships `bsdtar`, so
`tar xzf` works there out of the box.

Only the tag build attaches to a release. A `workflow_dispatch` run on a branch
still uploads a downloadable artifact, it just has no release to attach it to.

CI uses the built-in `GITHUB_TOKEN` with `permissions: contents: write`. No
personal access token is needed or wanted.

### Why there is no concurrency group

The three workflows deliberately do **not** share a `concurrency` group. An
earlier version gave all three `group: release-<tag>` with
`cancel-in-progress: false`, on the assumption that they would queue behind each
other. They do not: GitHub holds at most **one pending run per group**, so when
the third arrival showed up it displaced the queued second one, which was
cancelled with

```
Canceling since a higher priority waiting request for release-v0.1.1 exists
```

and that release shipped only two of its three platforms.

The group was there to stop the three racing to create the same draft. Without
it they can, so it is worth knowing the shape of that race.
`softprops/action-gh-release` with `draft: true` lists every release looking for
the tag — a draft is not returned by the get-release-by-tag endpoint — so the
normal path is still "first creates it, the other two attach". The gap is the
second or so between a draft being created and it becoming visible in that list:

```
👩‍🏭 Creating new GitHub release for tag v0.1.1...
Release 375124546 is not yet discoverable by tag v0.1.1, retrying... (2 retries remaining)
```

Two jobs entering that window together would both create, and GitHub permits
duplicate drafts for one tag because a draft's tag does not exist yet. In
practice they are minutes apart — `linux.yml` runs the tests first — so this is
about a one-second window in a several-minute build, but it is not zero.

**If you ever see two drafts for the same tag**, that is this race. Move the
assets onto whichever draft you intend to publish, delete the other, and publish.

## The scripts never call `gh`

`publish-release.sh` prints the Actions and Releases URLs and stops there. It
does not shell out to `gh`, on purpose: `gh` authenticates with its own keyring
account, which is not necessarily the account `origin` pushes as. Where they
differ, `gh` queries the API as the wrong user — and since draft releases are
only visible with push access, it will cheerfully report that the release you
just built does not exist. `gh run watch` with no run id is interactive on top of
that, which would hang a release that had already been pushed.

Watch the builds in the browser, or run `gh` yourself once you have checked
`gh auth status` against the account that owns the repo.

The URLs are derived from `git remote get-url` with any embedded credentials
stripped, so a remote carrying a token does not echo it to the terminal.

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
but" form, so `bundle.sh` walks `scripts/*.sh` and excludes everything except
`update.sh`. That list used to be written out by hand and it rotted immediately —
`bump-version.sh` and `publish-release.sh` shipped inside the v0.1.1 archives.
Generating it means a new script is excluded the moment it is added.

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

## When a release goes wrong

A tag build that fails part way leaves a tag, possibly a half-populated draft,
and a `release:` commit behind. There is no partial-retry: fix the cause, then
cut the next patch version. Re-using the tag means clearing all three.

```sh
# on GitHub: delete the draft release
git push origin :refs/tags/v1.3.0     # remove the remote tag
git tag -d v1.3.0                     # and the local one
```

`bump-version.sh` will not bump a version to itself, so if `cmd/version.go`
already reads `v1.3.0` you either move to `v1.3.1` or reset the release commit
before trying again.

## Checking a release

```sh
tar tzf emule-http-cache-v1.3.0-linux-amd64.tar.gz | sort
shasum -a 256 -c emule-http-cache-v1.3.0-linux-amd64.tar.gz.sha256

mkdir rel && tar -C rel -xzf emule-http-cache-v1.3.0-linux-amd64.tar.gz
cd rel && ./emule-http-cache version && ./emule-http-cache init && ./emule-http-cache serve
```
