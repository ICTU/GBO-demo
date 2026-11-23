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
export LC_ALL=C # one sort order for sort and comm

usage() {
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

EXCEPTIONS="scripts/intent-layer-exceptions.txt"
GUIDE="docs/intent-layer.md"

# Tracked and new (not ignored) files. Submodules such as vendor/nl-wallet
# are not expanded, so their files never show up here.
repo_files() {
  git ls-files --cached --others --exclude-standard -- "$@"
}

# Code size in tokens: bytes divided by 4, without tests, test data, images,
# lock files, Markdown and vendor/. A signal for comparing sizes, not a verdict.
code_tokens() {
  local bytes
  bytes="$(repo_files "$1" |
    grep -vE '(^|/)(vendor|node_modules|testdata|test|tests|__tests__)/|_test\.go$|\.(test|spec)\.[jt]sx?$|\.(png|jpe?g|gif|svg|ico|webp|pdf|md)$|(^|/)(go\.sum|package-lock\.json|yarn\.lock|pnpm-lock\.yaml|Cargo\.lock)$' |
    tr '\n' '\0' | xargs -0 cat 2>/dev/null | wc -c)"
  echo $((bytes / 4))
}

file_tokens() {
  echo $(($(wc -c <"$1") / 4))
}

cmd_size() {
  [ $# -eq 1 ] || usage
  local target="${1%/}"
  if [ -f "${target}" ]; then
    echo "${target}: $(file_tokens "${target}") tokens"
  elif [ -d "${target}" ]; then
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
  sed -E 's/#.*//; s/[[:space:]]+$//; s#/$##' "${EXCEPTIONS}" | grep -v '^$' | sort -u || true
}

cmd_check() {
  local errors=0 warnings=0 f node dir target code tokens nodes
  err() { echo "ERROR: $*"; errors=$((errors + 1)); }
  warn() { echo "WARNING: $*"; warnings=$((warnings + 1)); }

  # Every relative link in file $1 resolves against directory $2.
  check_links() {
    while IFS= read -r target; do
      [ -e "$2/${target%%#*}" ] || err "$1: link to ${target} does not resolve"
    done < <(md_links "$1")
  }

  # Only AGENTS.md: no tool-specific rule files next to or instead of it.
  while IFS= read -r f; do
    err "${f}: use AGENTS.md instead of a tool-specific rules file"
  done < <(repo_files | grep -E '(^|/)(CLAUDE\.md|GEMINI\.md|\.cursorrules)$' || true)

  nodes="$(repo_files | grep -E '(^|/)AGENTS\.md$' || true)"

  while IFS= read -r node; do
    if [ -L "${node}" ]; then
      err "${node}: is a symlink; write the node itself"
      continue
    fi
    [ -e "${node}" ] || continue
    case "${node}" in
      vendor/* | */vendor/* | */node_modules/* | */dist/* | */build/*)
        err "${node}: no nodes in vendored or generated directories" ;;
    esac

    dir="$(dirname "${node}")"
    check_links "${node}" "${dir}"

    # Size is only a warning: a node should be clearly smaller than the code
    # it covers. The root node has no target size.
    if [ "${node}" != "AGENTS.md" ]; then
      code="$(code_tokens "${dir}")"
      tokens="$(file_tokens "${node}")"
      if [ "${code}" -gt 0 ] && [ $((tokens * 10)) -gt "${code}" ]; then
        warn "${node}: ${tokens} tokens for ${code} tokens of code (over 10%); see ${GUIDE}"
      fi
    fi
  done < <(grep . <<<"${nodes}" || true)

  if [ -f "${GUIDE}" ]; then
    check_links "${GUIDE}" "$(dirname "${GUIDE}")"
  else
    err "${GUIDE} is missing"
  fi

  # Every boundary has a node or is on the exception list, and the list
  # names only boundaries that still have no node.
  local all with_node
  all="$(boundaries)"
  with_node="$(while IFS= read -r dir; do
    if [ -f "${dir}/AGENTS.md" ]; then echo "${dir}"; fi
  done <<<"${all}")"
  while IFS= read -r dir; do
    err "${dir}: has no AGENTS.md and is not in ${EXCEPTIONS}"
  done < <(comm -23 <(comm -23 <(echo "${all}") <(echo "${with_node}")) <(exceptions))
  while IFS= read -r dir; do
    err "${EXCEPTIONS}: ${dir} is not a service or frontend directory; remove it from the list"
  done < <(comm -13 <(echo "${all}") <(exceptions))
  while IFS= read -r dir; do
    err "${EXCEPTIONS}: ${dir} has an AGENTS.md; remove it from the list"
  done < <(comm -12 <(echo "${with_node}") <(exceptions))

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
