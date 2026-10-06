#!/bin/bash
set -euo pipefail

# Self-updater for an installed emule-http-cache release bundle.
#
# Detects the OS/arch, downloads the matching bundle of the latest published
# GitHub release, verifies its .sha256, installs it over the install directory,
# and starts the server.
#
# The script ships inside every bundle as scripts/update.sh and as a standalone
# release asset, so a fresh install can bootstrap with:
#
#   curl -fLO https://github.com/ModderMule/emule-http-cache-go/releases/latest/download/update.sh
#   bash update.sh
#
# Usage: ./scripts/update.sh [serve arguments...]
#
#   Any argument is passed on to `emule-http-cache serve`, e.g. --config.
#
# Environment:
#   UPDATE_REPO      GitHub owner/repo to update from (default ModderMule/emule-http-cache-go)
#   UPDATE_VERSION   install this tag (e.g. v0.1.3) instead of the latest release
#   UPDATE_NO_START  non-empty installs only and does not start the server
#   UPDATE_INSECURE  non-empty disables TLS verification (see below)
#
# config.yaml and data/ are never touched: no bundle contains either, and only
# the files a bundle does contain are replaced.
#
# Everything lives in main() and the file ends with `main "$@"; exit`. Bash
# reads a script incrementally, and this one replaces itself with the bundle's
# copy; parsing the whole function up front keeps that from executing a mix of
# old and new bytes.

usage() {
  sed -n 's/^# \{0,1\}//; /^Usage:/,/^Environment:/p' "$0" | sed '$d'
}

main() {
  case "${1:-}" in
    -h|--help) usage; exit 0 ;;
  esac

  # --- install dir ----------------------------------------------------------
  # A bundle carries this script as scripts/update.sh, so the install is one
  # level up. A standalone copy (the bootstrap download) installs beside itself.
  local DIR BUNDLED=""
  DIR="$(cd "$(dirname "$0")" && pwd)"
  if [[ "$(basename "$DIR")" == "scripts" ]]; then
    DIR="$(dirname "$DIR")"
    BUNDLED=1
  fi
  cd "$DIR"

  # The repo itself also contains scripts/update.sh; running it there would
  # spray a release bundle over the source tree. Bundles ship neither marker.
  if [[ -e "$DIR/.git" || -e "$DIR/go.mod" ]]; then
    echo "error: $DIR is a source checkout; copy update.sh into the install directory and run it there" >&2
    exit 1
  fi

  local REPO="${UPDATE_REPO:-ModderMule/emule-http-cache-go}"

  # --- platform -------------------------------------------------------------
  # Only the targets .github/workflows/{linux,macos,windows}.yml build. All
  # three are .tar.gz: the bundle subcommand emits nothing else.
  local OS ARCH PLAT EXE=""
  OS="$(uname -s)"
  ARCH="$(uname -m)"
  case "$OS/$ARCH" in
    Linux/x86_64|Linux/amd64)                  PLAT="linux-amd64" ;;
    Darwin/arm64|Darwin/aarch64)               PLAT="macos-arm64" ;;
    MINGW*/x86_64|MSYS*/x86_64|CYGWIN*/x86_64) PLAT="win64"; EXE=".exe" ;;
    *)
      echo "error: unsupported platform ${OS}/${ARCH}; supported: linux/amd64, macos/arm64, windows/amd64 (Git Bash)" >&2
      exit 1
      ;;
  esac

  # --- required tools -------------------------------------------------------
  need curl
  need tar
  local SHA_CMD
  if command -v sha256sum >/dev/null 2>&1; then
    SHA_CMD=(sha256sum)
  elif command -v shasum >/dev/null 2>&1; then
    SHA_CMD=(shasum -a 256)
  else
    echo "error: neither sha256sum nor shasum found; cannot verify the download" >&2
    exit 1
  fi

  # TLS verification stays on unless the operator opts out for a host with a
  # self-signed certificate. Turning it off by default would silently accept any
  # tarball a network attacker cared to serve, and this one is executed.
  CURL_OPTS=(-fsSL)
  if [[ -n "${UPDATE_INSECURE:-}" ]]; then
    echo "WARNING: UPDATE_INSECURE is set — the download is not authenticated."
    CURL_OPTS+=(--insecure)
  fi

  # --- target version -------------------------------------------------------
  # releases/latest skips drafts and pre-releases, so a tag only becomes visible
  # here once its draft has been published.
  local TAG="${UPDATE_VERSION:-}"
  if [[ -z "$TAG" ]]; then
    TAG="$(curl "${CURL_OPTS[@]}" "https://api.github.com/repos/${REPO}/releases/latest" \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)" || true
    if [[ -z "$TAG" ]]; then
      echo "error: could not determine the latest release of ${REPO} (no published release, or GitHub API rate limit)" >&2
      echo "       set UPDATE_VERSION=vX.Y.Z to pick a tag explicitly" >&2
      exit 1
    fi
  fi
  [[ "$TAG" == v* ]] || TAG="v${TAG}"

  local BIN="emule-http-cache${EXE}"
  local ASSET="emule-http-cache-${TAG}-${PLAT}.tar.gz"
  local BASE="https://github.com/${REPO}/releases/download/${TAG}"

  echo "Platform:  ${PLAT}"
  echo "Release:   ${REPO} ${TAG}"
  echo "Install:   ${DIR}"
  echo

  # --- download + verify into a staging dir ---------------------------------
  STAGE="$(mktemp -d)"
  trap 'rm -rf "$STAGE"' EXIT

  echo "Downloading ${ASSET} ..."
  if ! curl "${CURL_OPTS[@]}" -o "$STAGE/$ASSET" "$BASE/$ASSET" \
    || ! curl "${CURL_OPTS[@]}" -o "$STAGE/$ASSET.sha256" "$BASE/$ASSET.sha256"; then
    echo "error: could not download ${ASSET} from ${BASE}; nothing was installed" >&2
    exit 1
  fi

  # The .sha256 records a bare filename, so -c must run next to the archive.
  if ! (cd "$STAGE" && "${SHA_CMD[@]}" -c "$ASSET.sha256" >/dev/null); then
    echo "error: checksum mismatch for ${ASSET}; nothing was installed" >&2
    exit 1
  fi
  echo "Checksum OK."

  # The archive is flat -- the binary sits at its root -- so it gets a directory
  # of its own rather than unpacking beside the download.
  local SRC="$STAGE/root"
  mkdir "$SRC"
  tar -xzf "$STAGE/$ASSET" -C "$SRC"
  if [[ ! -f "$SRC/$BIN" ]]; then
    echo "error: ${ASSET} does not contain ${BIN}" >&2
    exit 1
  fi

  # --- install --------------------------------------------------------------
  # Each file is removed before it is copied. On Linux, writing into a running
  # binary fails with "Text file busy", and on macOS rewriting a signed binary in
  # place can get it killed; unlinking first gives the new file a fresh inode
  # while a running server keeps the old one. On Windows a running .exe is
  # locked and cannot be removed at all.
  local f rel
  while IFS= read -r f; do
    rel="${f#"$SRC"/}"
    mkdir -p "$DIR/$(dirname "$rel")"
    if ! { rm -f "$DIR/$rel" && cp -p "$f" "$DIR/$rel"; }; then
      echo "error: could not replace ${rel}; stop emule-http-cache first and re-run" >&2
      exit 1
    fi
  done < <(find "$SRC" -type f)
  chmod +x "$DIR/$BIN" "$DIR/scripts/update.sh" 2>/dev/null || true

  echo "Installed ${TAG}."
  if [[ -z "$BUNDLED" ]]; then
    echo "Update later with ./scripts/update.sh, which this release installed."
  fi
  echo

  # --- start ----------------------------------------------------------------
  if [[ -n "${UPDATE_NO_START:-}" ]]; then
    echo "UPDATE_NO_START is set; start it with ./${BIN} serve"
    exit 0
  fi
  echo "Starting ./${BIN} serve $*"
  exec "./${BIN}" serve "$@"
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: required tool '$1' not found" >&2
    exit 1
  fi
}

main "$@"; exit
