#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

if ! command -v swag >/dev/null 2>&1; then
  echo "The swag CLI is not on PATH. Install it with:"
  echo "  go install github.com/swaggo/swag/cmd/swag@latest"
  exit 1
fi

# The invocation itself lives in main.go's //go:generate line, so there is one
# definition of it rather than two that drift.
go generate ./...

echo "Regenerated docs_api/ — commit it, so a build does not need swag installed."
