#!/usr/bin/env bash
# Structural checks for the Intent Layer: the AGENTS.md files ("nodes") in
# this repository. docs/intent-layer.md explains what a node is and where
# one goes; this script only checks what must hold structurally.
#
#   scripts/intent-layer.sh check        fail (exit 1) on a structural error
#   scripts/intent-layer.sh size <path>  print the size of code or a file in tokens
#
# The check runs locally and in CI the same way: plain text output, exit 0
# when everything holds, 1 when something does not.
set -euo pipefail

ROOT="${INTENT_LAYER_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
EXCEPTIONS="${ROOT}/scripts/intent-layer-exceptions.txt"
GUIDE="docs/intent-layer.md"

usage() {
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

# Tracked and new (not ignored) files, relative to ROOT. Submodules such as
# vendor/nl-wallet are not expanded, so their files never show up here.
repo_files() {
  git -C "${ROOT}" ls-files --cached --others --exclude-standard
}

# Code size in tokens: bytes divided by 4, without tests, test data, images,
# lock files, Markdown and vendor/. A signal for comparing sizes, not a verdict.
code_tokens() {
  local dir="$1" bytes=0 f
  while IFS= read -r f; do
    [ -f "${ROOT}/${f}" ] || continue
    bytes=$((bytes + $(wc -c <"${ROOT}/${f}")))
  done < <(repo_files | grep -E "^${dir%/}/" | grep -vE \
    '(^|/)(vendor|node_modules|testdata|test|tests|__tests__)/|_test\.go$|\.(test|spec)\.[jt]sx?$|\.(png|jpe?g|gif|svg|ico|webp|pdf|md)$|(^|/)(go\.sum|package-lock\.json|yarn\.lock|pnpm-lock\.yaml|Cargo\.lock)$' || true)
  echo $((bytes / 4))
}

file_tokens() {
  echo $(($(wc -c <"$1") / 4))
}

cmd_size() {
  [ $# -eq 1 ] || usage
  local target="${1%/}"
  if [ -f "${ROOT}/${target}" ]; then
    echo "${target}: $(file_tokens "${ROOT}/${target}") tokens"
  elif [ -d "${ROOT}/${target}" ]; then
    echo "${target}: $(code_tokens "${target}") tokens of code"
  else
    echo "no such file or directory: ${target}" >&2
    exit 2
  fi
}

# Relative link targets of the Markdown links in a file, outside fenced code
# blocks and inline code. External links and pure anchors are left out.
md_links() {
  awk '
    /^[[:space:]]*(```|~~~)/ { fenced = !fenced; next }
    fenced { next }
    {
      line = $0
      gsub(/`[^`]*`/, "", line)
      while (match(line, /\]\([^)]+\)/)) {
        print substr(line, RSTART + 2, RLENGTH - 3)
        line = substr(line, RSTART + RLENGTH)
      }
    }' "$1" |
    sed -E 's/[[:space:]]+"[^"]*"$//' |
    grep -vE '^(https?:|mailto:|#)' || true
}

# Boundaries that need a node: every top-level directory and every directory
# directly under services/ that has its own Dockerfile, go.mod or package.json.
boundaries() {
  repo_files |
    grep -E '^([^/]+|services/[^/]+)/(Dockerfile|go\.mod|package\.json)$' |
    sed -E 's#/[^/]+$##' | sort -u
}

exceptions() {
  [ -f "${EXCEPTIONS}" ] || return 0
  sed -E 's/#.*//; s/[[:space:]]+$//; s#/$##' "${EXCEPTIONS}" | grep -v '^$' || true
}

cmd_check() {
  local errors=0 warnings=0 f node dir target code tokens
  err() { echo "ERROR: $*"; errors=$((errors + 1)); }
  warn() { echo "WARNING: $*"; warnings=$((warnings + 1)); }

  # Only AGENTS.md: no tool-specific rule files next to or instead of it.
  while IFS= read -r f; do
    err "${f}: use AGENTS.md instead of a tool-specific rules file"
  done < <(repo_files | grep -E '(^|/)(CLAUDE\.md|GEMINI\.md|\.cursorrules)$' || true)

  local nodes
  nodes="$(repo_files | grep -E '(^|/)AGENTS\.md$' || true)"

  while IFS= read -r node; do
    [ -n "${node}" ] || continue
    [ -e "${ROOT}/${node}" ] || [ -L "${ROOT}/${node}" ] || continue
    if [ -L "${ROOT}/${node}" ]; then
      err "${node}: is a symlink; write the node itself"
      continue
    fi
    case "${node}" in
      vendor/* | */vendor/* | */node_modules/* | */dist/* | */build/*)
        err "${node}: no nodes in vendored or generated directories" ;;
    esac

    # Every relative link resolves.
    dir="$(dirname "${node}")"
    while IFS= read -r target; do
      [ -n "${target}" ] || continue
      [ -e "${ROOT}/${dir}/${target%%#*}" ] || err "${node}: link to ${target} does not resolve"
    done < <(md_links "${ROOT}/${node}")

    # Size is only a warning: a node should be clearly smaller than the code
    # it covers. The root node has no target size.
    if [ "${node}" != "AGENTS.md" ]; then
      code="$(code_tokens "${dir}")"
      tokens="$(file_tokens "${ROOT}/${node}")"
      if [ "${code}" -gt 0 ] && [ $((tokens * 10)) -gt "${code}" ]; then
        warn "${node}: ${tokens} tokens for ${code} tokens of code (over 10%); see ${GUIDE}"
      fi
    fi
  done <<<"${nodes}"

  # Links in the guide itself resolve too.
  if [ -f "${ROOT}/${GUIDE}" ]; then
    while IFS= read -r target; do
      [ -n "${target}" ] || continue
      [ -e "${ROOT}/docs/${target%%#*}" ] || err "${GUIDE}: link to ${target} does not resolve"
    done < <(md_links "${ROOT}/${GUIDE}")
  else
    err "${GUIDE} is missing"
  fi

  # Every boundary has a node or is on the exception list, and the exception
  # list names only directories that exist and still have no node.
  local listed
  listed="$(exceptions)"
  while IFS= read -r dir; do
    [ -n "${dir}" ] || continue
    if [ ! -f "${ROOT}/${dir}/AGENTS.md" ] && ! grep -qxF "${dir}" <<<"${listed}"; then
      err "${dir}: has no AGENTS.md and is not in scripts/intent-layer-exceptions.txt"
    fi
  done < <(boundaries)
  while IFS= read -r dir; do
    [ -n "${dir}" ] || continue
    if [ ! -d "${ROOT}/${dir}" ]; then
      err "scripts/intent-layer-exceptions.txt: ${dir} does not exist"
    elif [ -f "${ROOT}/${dir}/AGENTS.md" ]; then
      err "scripts/intent-layer-exceptions.txt: ${dir} has an AGENTS.md; remove it from the list"
    fi
  done <<<"${listed}"

  echo "intent-layer check: $(grep -c . <<<"${nodes}") nodes, ${errors} errors, ${warnings} warnings"
  [ "${errors}" -eq 0 ]
}

case "${1:-}" in
  check)
    shift
    [ $# -eq 0 ] || usage
    cmd_check
    ;;
  size)
    shift
    cmd_size "$@"
    ;;
  *) usage ;;
esac
