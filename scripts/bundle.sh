#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

case "$1" in
  linux)
    export GOOS=linux
    export GOARCH=amd64
    echo "Building for linux/amd64"
    ;;
  *)
    echo "Building for your current OS"
    ;;
esac

"$PWD/scripts/build.sh"

# Remove env variables targeting other platforms so "go run" below executes on
# the host rather than trying to run a foreign binary.
unset GOOS
unset GOARCH

# The bundle ships everything the binary needs beside it: config.example.yaml
# for a hand install, the templates for the --static-file-path deploy, and the
# scripts. config.yaml is never included — it holds an upload credential.
DIRS="--exclude=.git --exclude=bin --exclude=data"
EXT="--ext=.go"
FILES="--files=go.mod --files=go.sum --files=config.yaml"
ADD="--add=$PWD/bin/emule-http-cache"
go run . bundle "--out=./bin/emule-http-cache.tar.gz" $DIRS $EXT $FILES $ADD

echo "Bundled bin/emule-http-cache.tar.gz"
