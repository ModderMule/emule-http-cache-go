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

# --- where to watch ---------------------------------------------------------
# Deliberately no `gh` here. gh authenticates with its own keyring account,
# which is not necessarily the account origin pushes as -- on this machine it is
# not -- so it would query the API under the wrong identity, and cannot see a
# draft release at all without push access on the repo. `gh run watch` with no
# run id is interactive too, which would hang an otherwise finished release.
#
# There is also no `gh workflow run`: the tag push above already started all
# three workflows, and dispatching would build every release twice.
#
# The sed strips any credentials embedded in the remote URL before printing it.
# origin may carry a token, and it must not be echoed to the terminal.
REPO_URL="$(git remote get-url "${REMOTE}" \
  | sed -E 's#^https://[^@/]*@#https://#; s#^git@github\.com:#https://github.com/#; s#\.git$##')"

echo
echo "Actions:  ${REPO_URL}/actions"
echo "Releases: ${REPO_URL}/releases"
