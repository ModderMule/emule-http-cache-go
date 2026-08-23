#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

# The platform label doubles as the archive suffix, so it matches what the
# release workflows publish: linux-amd64, macos-arm64, win64.
case "$1" in
  linux)
    export GOOS=linux
    export GOARCH=amd64
    PLATFORM="linux-amd64"
    ;;
  macos|darwin)
    export GOOS=darwin
    export GOARCH=arm64
    PLATFORM="macos-arm64"
    ;;
  windows|win64)
    export GOOS=windows
    export GOARCH=amd64
    PLATFORM="win64"
    ;;
  "")
    PLATFORM="$(go env GOOS)-$(go env GOARCH)"
    case "$PLATFORM" in
      darwin-*)  PLATFORM="macos-${PLATFORM#darwin-}" ;;
      windows-amd64) PLATFORM="win64" ;;
    esac
    echo "Building for your current OS ($PLATFORM)"
    ;;
  *)
    echo "Usage: $0 [linux|macos|windows]   (no argument builds for the host)"
    exit 1
    ;;
esac

./scripts/build.sh

BIN_NAME="emule-http-cache"
if [ "${GOOS:-$(go env GOOS)}" = "windows" ]; then
  BIN_NAME="${BIN_NAME}.exe"
fi

# Remove env variables targeting other platforms so "go run" below executes on
# the host rather than trying to run a foreign binary.
unset GOOS
unset GOARCH

VERSION="${VERSION:-$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/version.go | head -1)}"
VERSION="${VERSION:-dev}"
OUT="${OUT:-./dist/emule-http-cache-${VERSION}-${PLATFORM}.tar.gz}"
mkdir -p "$(dirname "$OUT")"

# The bundle ships everything the binary needs beside it: config.example.yaml
# for a hand install, the templates for the --static-file-path deploy, and
# scripts/update.sh so a deployed copy can pull the next release over itself.
# config.yaml is never included — it holds an upload credential.
#
# Only update.sh ships from scripts/; the rest are build-time helpers with no
# business in a deploy. pkg/bundle excludes by base name and has no "everything
# but" form, so a newly added script must be named here or it goes out too.
DIRS="--exclude=.git --exclude=bin --exclude=dist --exclude=data --exclude=testdata"
EXT="--ext=.go"
FILES="--files=go.mod --files=go.sum --files=config.yaml"
FILES="$FILES --files=build.sh --files=bundle.sh --files=swagger.sh --files=test.sh"
ADD="--add=bin/$BIN_NAME"
go run . bundle "--out=$OUT" $DIRS $EXT $FILES $ADD

echo "Bundled $OUT"
