#!/bin/bash
set -euo pipefail

# Interactive release helper for emule-http-cache.
#
# Delegates the version bump, commit and tag to scripts/bump-version.sh, then
# pushes the branch and the tag. The tag push is what starts the three build
# workflows; each uploads its bundle to a DRAFT GitHub Release, which you then
# write notes for and publish by hand.
#
# Usage: scripts/publish-release.sh [VERSION]

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

# --- guard: clean working tree ---------------------------------------------
# bump-version.sh checks this too, but failing here means we have not touched
# anything yet and there is nothing to unwind.
if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree has uncommitted changes; commit or stash them first" >&2
  git status --short >&2
  exit 1
fi

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
REMOTE="origin"

# --- bump, commit, tag ------------------------------------------------------
./scripts/bump-version.sh "$@"

NEW="$(git describe --tags --exact-match 2>/dev/null || true)"
if [ -z "$NEW" ]; then
  echo "error: bump-version.sh left no tag on HEAD; nothing to push" >&2
  exit 1
fi

# --- confirm ----------------------------------------------------------------
echo
echo "About to push:"
echo "  branch : ${REMOTE} ${BRANCH}"
echo "  tag    : ${REMOTE} ${NEW}"
echo
read -r -p "Proceed? [y/N]: " CONFIRM
# tr rather than ${CONFIRM,,}: that expansion is bash 4+, and macOS ships 3.2.
CONFIRM="$(printf '%s' "$CONFIRM" | tr '[:upper:]' '[:lower:]')"
if [ "$CONFIRM" != "y" ]; then
  echo "aborted; undoing the local commit and tag"
  git tag -d "${NEW}"
  git reset --hard HEAD~1
  exit 1
fi

# --- push -------------------------------------------------------------------
git push "${REMOTE}" "${BRANCH}"
git push "${REMOTE}" "${NEW}"

echo
echo "Pushed ${NEW}."
echo "The tag push starts .github/workflows/{linux,macos,windows}.yml; each"
echo "attaches its bundle to a DRAFT release for ${NEW}."
echo
echo "Finish with: write the notes on the draft, then publish it."

# --- optional: follow the builds --------------------------------------------
# No `gh workflow run` here: the tag push above already started all three, and
# dispatching would build every release twice. `|| true` throughout, since
# set -e is in effect and the tag is already pushed by this point -- a gh
# hiccup must not report an otherwise-successful release as failed.
if command -v gh >/dev/null 2>&1; then
  read -r -p "Watch the builds now? [y/N]: " WATCH
  WATCH="$(printf '%s' "$WATCH" | tr '[:upper:]' '[:lower:]')"
  if [ "$WATCH" = "y" ]; then
    gh run list --limit 3 || true
    gh run watch || true
  else
    echo "Follow them with: gh run list --limit 3"
  fi
fi
