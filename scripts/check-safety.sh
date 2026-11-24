#!/usr/bin/env bash
# Safety audit: real OINs, internal hostnames and secrets must not be in
# this public repository. Scans tracked files only. HIGH findings exit 1;
# LOW findings are informational (-v lists them).
# No allowlist: replace a flagged test value (demo OINs start with
# 99999999), remove a real one.
#
#   scripts/check-safety.sh [-v] [path]    # or: make check-safety

set -euo pipefail

verbose=0
if [[ "${1:-}" == "-v" ]]; then
  verbose=1
  shift
fi

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

TARGET="${1:-.}"

# This script holds the patterns; lockfiles and images only add noise.
EXCLUDES=(':!scripts/check-safety.sh' ':!*package-lock.json' ':!*go.sum' ':!*.svg' ':!*.png')

file_count=$(git grep -l -e '' -- "$TARGET" "${EXCLUDES[@]}" | grep -c '' || true)
if [[ $file_count -eq 0 ]]; then
  echo "check-safety: no tracked files under $TARGET" >&2
  exit 2
fi

# git grep skips untracked files and submodules; it exits 1 on no match.
scan() {
  git grep -IPn -e "$1" -- "$TARGET" "${EXCLUDES[@]}" || true
}

# ── HIGH severity (blocking) ────────────────────────────────────────────

high_hits=0

add_high() {
  local label="$1" matches="$2"
  [[ -n "$matches" ]] || return 0
  high_hits=$((high_hits + $(grep -c '' <<< "$matches")))
  printf '\n=== HIGH: %s ===\n%s\n' "$label" "$matches"
}

check_high() {
  add_high "$1" "$(scan "$2")"
}

# Real OINs start with 0000000[1-9]; 99999999… is the demo range.
check_high "Non-demo OIN" '(?<![0-9])(?!99999999|00000000)[0-9]{20}(?![0-9])'

# Host position only, so code like f.internal.URL does not match.
check_high "Corporate/VPN hostnames" '(?:(?:://|@|["\x27`=]|:\s+)[a-zA-Z0-9.-]*|\b[a-zA-Z0-9.-]+(?=\.(?:internal|corp):[0-9]))(?<!host\.docker)\.(?:internal|corp)\b(?![\w.-])'

check_high "Tunnel URLs" 'https?://[a-z0-9-]+\.trycloudflare\.com|https?://[a-z0-9-]+\.ngrok\.io|https?://[a-z0-9-]+\.ngrok-free\.app'

check_high "Hardcoded secret material" '(api[_-]?key|secret|password|passwd|access[_-]?token)\s*[:=]\s*["\x27][A-Za-z0-9_\-+/]{20,}["\x27]'

# ${VAR} and <placeholder> passwords are fine.
check_high "URL with embedded credentials" '(postgres|postgresql|mysql|mongodb|https?|redis)://[^:/\s"\x27$]+:(?!\$\{|<)[^@/\s"\x27]+@'

check_high "Inline private_key / certificate in config" '(private_key|certificate)\s*[:=]\s*"(?!REPLACE_)[A-Za-z0-9+/=_-]{40,}"'

# A BEGIN line on its own, so tests that mention the marker do not match.
check_high "PEM private key" '^\s*-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY-----\s*$'

# Demo PKI material lives under pki/ and certs/.
strays=$(git ls-files -- "$TARGET" |
  grep -E '\.(key|p12|pfx|jks)$|key\.pem$' |
  grep -vE '(^|/)(pki|certs)/' || true)
add_high "Private-key file outside a pki/ or certs/ directory" "$strays"

# ── LOW severity (informational) ────────────────────────────────────────

low_summary=""

add_low() {
  local label="$1" matches="$2"
  [[ -n "$matches" ]] || return 0
  low_summary+="LOW:  $(grep -c '' <<< "$matches")  $label"$'\n'
  if [[ $verbose -eq 1 ]]; then
    printf '\n--- LOW: %s ---\n%s\n' "$label" "$matches"
  fi
}

check_low() {
  add_low "$1" "$(scan "$2")"
}

check_low ".local hostnames (test fixtures likely)" '\b[a-zA-Z0-9-]+\.local\b'
check_low "9-digit numbers (potential BSNs, verify all are mock)" '(?<![0-9])[0-9]{9}(?![0-9])'
check_low "Common Dutch first names in text" '\b(Jan|Piet|Kees|Marieke|Sanne|Wouter|Anouk|Bart|Femke|Sven|Lars|Jasper|Rob|Hans|Karin|Linda|Maud)\b'

# Markdown only: code has npm scopes like @types/x.
add_low "GitHub @-mentions in markdown" \
  "$(scan '(?<![\w.])@[a-zA-Z][a-zA-Z0-9_-]{2,}\b' | grep -E '^[^:]*\.md:' || true)"

# ── Report ──────────────────────────────────────────────────────────────

echo ""
echo "check-safety: scanned $file_count tracked files under $TARGET"
printf '%s' "$low_summary"
if [[ -n "$low_summary" && $verbose -eq 0 ]]; then
  echo "      (informational; run with -v to list them)"
fi
if [[ $high_hits -gt 0 ]]; then
  echo "HIGH: $high_hits blocking finding(s). Remove a real value; replace a test"
  echo "      or demo value with one the check accepts (see the script header)."
  exit 1
fi
echo "OK: no blocking findings."
