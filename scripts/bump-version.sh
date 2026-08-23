#!/bin/bash
set -euo pipefail

# Bumps the version literal in cmd/version.go -- the single source of truth for
# the version -- then creates a release commit and an annotated git tag. Local
# only: nothing is pushed. scripts/publish-release.sh wraps this and pushes.
#
# The build workflows (.github/workflows/{linux,macos,windows}.yml) read that
# same literal to name their artifacts, so tag, archive filename and the version
# the binary reports all stay in lockstep.
#
# protocolVersion in http_public/info.go is deliberately NOT bumped here; it is
# the wire contract clients handshake on and changes only when the contract does.
#
# Usage: scripts/bump-version.sh [VERSION]

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

VERSION_FILE="cmd/version.go"
# Anchored on purpose: the doc comment above the literal carries a
# "cmd.Version=1.0.0" ldflags example, and commit() calls runtime.Version().
VERSION_SED='s/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p'

# --- guard: clean working tree ---------------------------------------------
if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree has uncommitted changes; commit or stash them first" >&2
  git status --short >&2
  exit 1
fi

# --- current version + suggested next patch bump ---------------------------
CURRENT="$(sed -n "$VERSION_SED" "$VERSION_FILE" | head -1)"
CURRENT="${CURRENT:-v0.0.0}"

suggest="v0.0.1"
if [[ "$CURRENT" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  suggest="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$(( BASH_REMATCH[3] + 1 ))"
fi

# --- the new version: argument, or prompted with the suggestion as default --
NEW="${1:-}"
if [ -z "$NEW" ]; then
  echo "Current version: ${CURRENT}"
  read -r -p "New version [${suggest}]: " NEW
  NEW="${NEW:-$suggest}"
fi
# normalize a leading 'v'
[[ "$NEW" == v* ]] || NEW="v${NEW}"

if [[ ! "$NEW" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: '${NEW}' is not a valid vMAJOR.MINOR.PATCH version" >&2
  exit 1
fi

if git rev-parse -q --verify "refs/tags/${NEW}" >/dev/null; then
  echo "error: tag ${NEW} already exists" >&2
  exit 1
fi

# --- record the version in the source of truth ------------------------------
# -i.bak with an explicit suffix, then rm: BSD sed on macOS requires an argument
# to -i, GNU sed does not, and this form is the one both accept.
sed -i.bak "s#^\([[:space:]]*Version[[:space:]]*=[[:space:]]*\)\"[^\"]*\"#\1\"${NEW}\"#" "$VERSION_FILE"
rm -f "${VERSION_FILE}.bak"

# Read it back rather than trusting the substitution. This is the one check that
# catches a pattern which silently stopped matching after the file was edited --
# without it a release would ship binaries still reporting the previous version,
# and nothing else in the build would notice.
GOT="$(sed -n "$VERSION_SED" "$VERSION_FILE" | head -1)"
if [ "$GOT" != "$NEW" ]; then
  echo "error: failed to bump Version in ${VERSION_FILE} (got '${GOT}')" >&2
  git checkout -- "$VERSION_FILE"
  exit 1
fi

# --- commit and tag, locally ------------------------------------------------
git add "$VERSION_FILE"
git commit -m "release: ${NEW}"
git tag -a "${NEW}" -m "Release ${NEW}"

echo "Bumped ${CURRENT} -> ${NEW}, committed and tagged locally."
echo "Push it with: ./scripts/publish-release.sh   (or: git push origin HEAD && git push origin ${NEW})"
