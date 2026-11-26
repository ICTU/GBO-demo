#!/usr/bin/env bash
# Format the Markdown files in this repository with oxfmt, so every file
# wraps at 80 columns and uses the same list, emphasis and table style.
#
#   scripts/md-format.sh fmt     rewrite the files in place
#   scripts/md-format.sh check   fail (exit 1) when a file is not formatted
#
# Settings live in .oxfmtrc.json, exclusions in .prettierignore (oxfmt reads
# both, and its Markdown output equals Prettier's with the same options).
# Code blocks (mermaid included) are never touched. Needs Node.js (npx);
# oxfmt runs at the pinned version below, without a global install.
set -euo pipefail

OXFMT_VERSION="0.80.0"

usage() {
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

[ $# -eq 1 ] || usage
case "$1" in
  fmt) mode="--write" ;;
  check) mode="--check" ;;
  *) usage ;;
esac

cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

command -v npx >/dev/null 2>&1 || {
  echo "npx not found: install Node.js to format Markdown" >&2
  exit 2
}

# Tracked and new (not ignored) Markdown files. Submodules such as
# vendor/nl-wallet are not expanded, so their files never show up here.
files=()
while IFS= read -r -d '' f; do
  files+=("${f}")
done < <(git ls-files -z --cached --others --exclude-standard -- '*.md')

if ! npx --yes "oxfmt@${OXFMT_VERSION}" "${mode}" --disable-nested-config \
  -- "${files[@]}"; then
  if [ "$1" = check ]; then
    echo "Markdown is not formatted: run 'make md-fmt' and commit the result." >&2
  fi
  exit 1
fi
