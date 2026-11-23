#!/usr/bin/env bash
# Safety audit: content that must not be in this public repository.
#
# Grep-based checks over every file tracked by git (untracked files,
# git-ignored runtime config and submodules such as vendor/nl-wallet are
# not scanned):
#
#   1. Real OINs (organisatie-identificatienummers); only demo numbers are
#      allowed.
#   2. Internal / VPN-only hostnames and personal tunnel URLs.
#   3. Secrets: API keys, tokens, passwords, credentials in URLs, inline
#      private keys or certificates, private-key files.
#   4. (informational) .local hostnames, 9-digit numbers that could be a
#      BSN (burgerservicenummer), first names, @-mentions.
#
# HIGH findings are blocking: the script exits 1. LOW findings are
# informational: a count per category by default, the full list with -v.
#
# There is no allowlist: when a check flags a test or demo value, replace
# it with one the check accepts (an OIN from the demo range 99999999...,
# an identifier or placeholder that does not look like a secret). When it
# flags a real value, remove the value instead.
#
# Runs in CI on every change. Run it locally from anywhere in the repo:
#
#   make check-safety                  # or: scripts/check-safety.sh
#   scripts/check-safety.sh -v         # also list the LOW findings
#   scripts/check-safety.sh services/  # audit tracked files under a path
#
# Needs bash and a git that supports git grep -P (built with PCRE), as
# Linux distributions and Homebrew ship it; Apple's own git may lack it.
#
# Exit codes: 0 no blocking findings, 1 blocking findings, 2 usage error.

set -euo pipefail

verbose=0
if [[ "${1:-}" == "-v" ]]; then
  verbose=1
  shift
fi

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

TARGET="${1:-.}"

# git grep searches only files tracked by git and skips submodules.
# -P (Perl regular expressions) needs a git built with PCRE, as on Linux
# distributions, Homebrew and CI runners; stop if this one lacks it.
if ! git grep -qP -e '' -- scripts/check-safety.sh 2>/dev/null; then
  echo "check-safety: this git cannot search with -P (built without PCRE)." >&2
  exit 2
fi

# Files that are not worth scanning: this script holds the patterns,
# lockfiles and checksum files hold only hashes, images are not text.
EXCLUDES=(':!scripts/check-safety.sh' ':!*package-lock.json' ':!*go.sum' ':!*.svg' ':!*.png')

# Count the files that will be scanned (every file with at least one
# line); none means a mistyped path or one without tracked files.
file_count=$(git grep -l -e '' -- "$TARGET" "${EXCLUDES[@]}" | grep -c '' || true)
if [[ $file_count -eq 0 ]]; then
  echo "check-safety: no tracked files under $TARGET" >&2
  exit 2
fi

# Print every line that matches a pattern, as "file:line:text".
# -I skips binary files; git grep exits 1 when nothing matches.
scan() {
  git grep -IPn -e "$1" -- "$TARGET" "${EXCLUDES[@]}" || true
}

# ── HIGH severity (blocking) ────────────────────────────────────────────

high_hits=0

# Print the findings of one check and count them.
add_high() {
  local label="$1" matches="$2"
  [[ -n "$matches" ]] || return 0
  high_hits=$((high_hits + $(grep -c '' <<< "$matches")))
  printf '\n=== HIGH: %s ===\n%s\n' "$label" "$matches"
}

check_high() {
  add_high "$1" "$(scan "$2")"
}

# OIN: 20 digits. Real OINs start with 0000000 followed by 1-9 (the
# register prefix), so those are not exempt. Exempt: the demo range
# 99999999..., and 00000000... (no real OIN starts with eight zeros).
# Test and demo data use the 99999999... range.
check_high "Non-demo OIN" '(?<![0-9])(?!99999999|00000000)[0-9]{20}(?![0-9])'

# Corporate / VPN-only hostnames. Only in host position (after "://",
# "@", a quote, "=" or "key: ", or followed by a port), so code such as
# f.internal.URL does not match. host.docker.internal is Docker's public alias for the host.
check_high "Corporate/VPN hostnames" '(?:(?:://|@|["\x27`=]|:\s+)[a-zA-Z0-9.-]*|\b[a-zA-Z0-9.-]+(?=\.(?:internal|corp):[0-9]))(?<!host\.docker)\.(?:internal|corp)\b(?![\w.-])'

# Cloudflared / ngrok tunnels (personal development environments).
check_high "Tunnel URLs" 'https?://[a-z0-9-]+\.trycloudflare\.com|https?://[a-z0-9-]+\.ngrok\.io|https?://[a-z0-9-]+\.ngrok-free\.app'

# API keys / tokens / passwords assigned a literal of 20+ characters.
check_high "Hardcoded secret material" '(api[_-]?key|secret|password|passwd|access[_-]?token)\s*[:=]\s*["\x27][A-Za-z0-9_\-+/]{20,}["\x27]'

# Credentials in connection URLs (postgres://user:pass@host). Skipped: an
# environment-variable password (${VAR}) and a <placeholder> password.
check_high "URL with embedded credentials" '(postgres|postgresql|mysql|mongodb|https?|redis)://[^:/\s"\x27$]+:(?!\$\{|<)[^@/\s"\x27]+@'

# Inline private-key or certificate material in TOML / JSON / YAML config,
# such as private_key = "MIGHAgEAM..." with 40+ base64 characters.
# Placeholder text (REPLACE_WITH_...) is allowed.
check_high "Inline private_key / certificate in config" '(private_key|certificate)\s*[:=]\s*"(?!REPLACE_)[A-Za-z0-9+/=_-]{40,}"'

# A PEM private-key block: the BEGIN line on its own, as in a key file.
# A BEGIN marker inside a string (a test that checks for it) does not match.
check_high "PEM private key" '^\s*-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY-----\s*$'

# Private-key files outside the demo PKI directories (pki/, certs/).
strays=$(git ls-files -- "$TARGET" |
  grep -E '\.(key|p12|pfx|jks)$|key\.pem$' |
  grep -vE '(^|/)(pki|certs)/' || true)
add_high "Private-key file outside a pki/ or certs/ directory" "$strays"

# ── LOW severity (informational) ────────────────────────────────────────

low_summary=""

# Count the findings of one check for the summary; list them with -v.
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

# GitHub @-mentions, in markdown only (code has npm scopes like @types/x).
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
