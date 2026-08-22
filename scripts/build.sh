#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

# Stamp the version so `emule-http-cache version` says something useful. A tag
# if we are on one, otherwise the short commit.
VERSION="${VERSION:-$(git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo dev)}"
# --verify keeps this empty in a repo with no commits yet, where a bare
# rev-parse prints the literal string "HEAD".
COMMIT="$(git rev-parse --verify -q HEAD 2>/dev/null || true)"
PKG="github.com/ModderMule/emule-http-cache-go/cmd"

mkdir -p "$PWD/bin"
go build -ldflags "-X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT}" -o "$PWD/bin/emule-http-cache"

echo "Built bin/emule-http-cache ${VERSION}"
