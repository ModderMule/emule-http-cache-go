#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

gofmt -l . | grep -v '^$' && { echo "gofmt found unformatted files (above)."; exit 1; } || true
go vet ./...
go test ./... "$@"

# The contract test doubles as a check against any other implementation. Point
# it at one to prove this port and that one agree:
#
#   go test ./internal/conformance -base http://localhost/emule-http-cache-php -key <key>
