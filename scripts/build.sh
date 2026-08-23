#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

# The version literal in cmd/version.go is the single source of truth: the same
# value scripts/bump-version.sh writes and the build workflows scrape to name
# their archives. The pattern is anchored on purpose — the doc comment above it
# contains a "cmd.Version=1.0.0" example an unanchored match would pick up.
VERSION="${VERSION:-$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/version.go | head -1)}"
VERSION="${VERSION:-dev}"
# --verify keeps this empty in a repo with no commits yet, where a bare
# rev-parse prints the literal string "HEAD".
COMMIT="$(git rev-parse --verify -q HEAD 2>/dev/null || true)"
PKG="github.com/ModderMule/emule-http-cache-go/cmd"

# Honour a cross-compile from the environment. `go env` answers for the host
# when GOOS is unset, so TARGET_OS is always the OS actually being built for.
TARGET_OS="${GOOS:-$(go env GOOS)}"
BIN_DIR="${BIN_DIR:-bin}"
BIN_NAME="emule-http-cache"
if [ "$TARGET_OS" = "windows" ]; then
  BIN_NAME="${BIN_NAME}.exe"
fi

# -trimpath keeps absolute build paths out of the binary, and -s -w drop the
# symbol table and DWARF for a smaller download. Neither disturbs
# debug.ReadBuildInfo(), so the vcs.revision fallback in commit() survives.
mkdir -p "$BIN_DIR"
go build -trimpath \
  -ldflags "-s -w -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT}" \
  -o "${BIN_DIR}/${BIN_NAME}"

echo "Built ${BIN_DIR}/${BIN_NAME} ${VERSION}"
