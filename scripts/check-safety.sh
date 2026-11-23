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
# Known-good matches are listed, each with a reason, in
# scripts/check-safety.allowlist. Add an entry there only for a match that
# is not a leak; never add a real value.
#
# Runs in CI on every change. Run it locally from anywhere in the repo:
#
#   make check-safety                  # or: scripts/check-safety.sh
#   scripts/check-safety.sh -v         # also list the LOW findings
#   scripts/check-safety.sh services/  # audit tracked files under a path
#
# Needs bash, git and GNU grep (for -P). On macOS: brew install grep, and
# put its gnubin directory first on PATH.
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
if [[ ! -e "$TARGET" ]]; then
  echo "check-safety: path not found (relative to the repo root): $TARGET" >&2
  exit 2
fi

if ! echo "" | grep -P '' > /dev/null 2>&1; then
  echo "check-safety: this script requires GNU grep (-P support)." >&2
  echo "On macOS: brew install grep, and put its gnubin directory first on PATH." >&2
  exit 2
fi

ALLOWLIST="scripts/check-safety.allowlist"

# Tracked regular files under TARGET, minus files that cannot leak in a
# meaningful way or would only produce noise: this script and its allowlist
# (they hold the patterns), lockfiles and checksum files (hashes).
FILES=()
while IFS= read -r -d '' f; do
  [[ -f "$f" && ! -L "$f" ]] || continue
  case "$f" in
    scripts/check-safety.sh | "$ALLOWLIST") continue ;;
    *package-lock.json | *go.sum | *.svg | *.png) continue ;;
  esac
  FILES+=("$f")
done < <(git ls-files -z -- "$TARGET")

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "check-safety: no tracked files under $TARGET" >&2
  exit 2
fi

# Allowlist: lines "<path glob>|<fixed text>". A finding is dropped when
# its file matches the glob and its line contains the text.
allow_globs=()
allow_texts=()
if [[ -f "$ALLOWLIST" ]]; then
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "${line// /}" || "$line" == \#* ]] && continue
    allow_globs+=("${line%%|*}")
    allow_texts+=("${line#*|}")
  done < "$ALLOWLIST"
fi

# Reads grep -n output ("file:line:text") on stdin, prints the findings
# that are not allowlisted.
filter_allowed() {
  local hit file text i allowed
  while IFS= read -r hit; do
    file="${hit%%:*}"
    text="${hit#*:}"
    text="${text#*:}"
    allowed=0
    for i in "${!allow_globs[@]}"; do
      # shellcheck disable=SC2254 # the glob is meant to match as a pattern
      case "$file" in
        ${allow_globs[$i]})
          if [[ "$text" == *"${allow_texts[$i]}"* ]]; then
            allowed=1
            break
          fi
          ;;
      esac
    done
    [[ $allowed -eq 1 ]] || printf '%s\n' "$hit"
  done
}

scan() {
  # -I skips binary files; grep exits 1 when nothing matches.
  printf '%s\0' "${FILES[@]}" | xargs -0 grep -IHPn -- "$1" 2>/dev/null | filter_allowed || true
}

# ── HIGH severity (blocking) ────────────────────────────────────────────

high_hits=0
high_report=""

add_high() {
  local label="$1" matches="$2" count
  [[ -n "$matches" ]] || return 0
  count=$(printf '%s\n' "$matches" | wc -l | tr -d ' ')
  high_hits=$((high_hits + count))
  high_report+=$'\n=== HIGH: '"$label"$' ===\n'"$matches"$'\n'
}

check_high() {
  add_high "$1" "$(scan "$2")"
}

# OIN: 20 digits. Real OINs start with 0000000 followed by 1-9 (the
# register prefix), so those are not exempt. Exempt: the demo range
# 99999999..., and 00000000... (no real OIN starts with eight zeros).
# Synthetic numbers with a real-looking prefix are listed in the allowlist.
check_high "Non-demo OIN" '(?<![0-9])(?!99999999|00000000)[0-9]{20}(?![0-9])'

# Corporate / VPN-only hostnames. Only in host position (after "://",
# "@", a quote, "=" or "key: ", or followed by a port), so code such as
# f.internal.URL does not match. host.docker.internal is Docker's public alias for the host.
check_high "Corporate/VPN hostnames" '(?:(?:://|@|["'\''`=]|:\s+)[a-zA-Z0-9.-]*|\b[a-zA-Z0-9.-]+(?=\.(?:internal|corp):[0-9]))(?<!host\.docker)\.(?:internal|corp)\b(?![\w.-])'

# Cloudflared / ngrok tunnels (personal development environments).
check_high "Tunnel URLs" 'https?://[a-z0-9-]+\.trycloudflare\.com|https?://[a-z0-9-]+\.ngrok\.io|https?://[a-z0-9-]+\.ngrok-free\.app'

# API keys / tokens / passwords assigned a literal of 20+ characters.
check_high "Hardcoded secret material" '(api[_-]?key|secret|password|passwd|access[_-]?token)\s*[:=]\s*["'\''][A-Za-z0-9_\-+/]{20,}["'\'']'

# Credentials in connection URLs (postgres://user:pass@host). Skipped: an
# environment-variable password (${VAR}) and a <placeholder> password.
check_high "URL with embedded credentials" '(postgres|postgresql|mysql|mongodb|https?|redis)://[^:/\s"'\''$]+:(?!\$\{|<)[^@/\s"'\'']+@'

# Inline private-key or certificate material in TOML / JSON / YAML config,
# such as private_key = "MIGHAgEAM..." with 40+ base64 characters.
# Placeholder text (REPLACE_WITH_...) is allowed.
check_high "Inline private_key / certificate in config" '(private_key|certificate)\s*[:=]\s*"(?!REPLACE_)[A-Za-z0-9+/=_-]{40,}"'

# A PEM private-key block: the BEGIN line on its own, as in a key file.
# A BEGIN marker inside a string (a test that checks for it) does not match.
check_high "PEM private key" '^\s*-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY-----\s*$'

# Private-key files outside the demo PKI directories (pki/, certs/).
strays=$(printf '%s\n' "${FILES[@]}" |
  grep -E '\.(key|p12|pfx|jks)$|key\.pem$' |
  grep -vE '(^|/)(pki|certs)/' || true)
add_high "Private-key file outside a pki/ or certs/ directory" "$strays"

# ── LOW severity (informational) ────────────────────────────────────────

low_report=""
low_summary=""

check_low() {
  local label="$1" matches count
  matches=$(scan "$2")
  [[ -n "$matches" ]] || return 0
  count=$(printf '%s\n' "$matches" | wc -l | tr -d ' ')
  low_summary+="LOW:  $count  $label"$'\n'
  low_report+=$'\n--- LOW: '"$label"$' ---\n'"$matches"$'\n'
}

check_low ".local hostnames (test fixtures likely)" '\b[a-zA-Z0-9-]+\.local\b'
check_low "9-digit numbers (potential BSNs, verify all are mock)" '(?<![0-9])[0-9]{9}(?![0-9])'
check_low "Common Dutch first names in text" '\b(Jan|Piet|Kees|Marieke|Sanne|Wouter|Anouk|Bart|Femke|Sven|Lars|Jasper|Rob|Hans|Karin|Linda|Maud)\b'

# GitHub @-mentions, in markdown only (code has npm scopes like @types/x).
mentions=$(printf '%s\n' "${FILES[@]}" | grep -E '\.md$' | tr '\n' '\0' |
  xargs -0 -r grep -IHPn '(?<![\w.])@[a-zA-Z][a-zA-Z0-9_-]{2,}\b' 2>/dev/null | filter_allowed || true)
if [[ -n "$mentions" ]]; then
  low_summary+="LOW:  $(printf '%s\n' "$mentions" | wc -l | tr -d ' ')  GitHub @-mentions in markdown"$'\n'
  low_report+=$'\n--- LOW: GitHub @-mentions in markdown ---\n'"$mentions"$'\n'
fi

# ── Report ──────────────────────────────────────────────────────────────

printf '%s' "$high_report"
if [[ $verbose -eq 1 ]]; then
  printf '%s' "$low_report"
fi

echo ""
echo "check-safety: scanned ${#FILES[@]} tracked files under $TARGET"
printf '%s' "$low_summary"
if [[ -n "$low_summary" && $verbose -eq 0 ]]; then
  echo "      (informational; run with -v to list them)"
fi
if [[ $high_hits -gt 0 ]]; then
  echo "HIGH: $high_hits blocking finding(s). Remove the value, or, if it is"
  echo "      not a leak, add it with a reason to $ALLOWLIST."
  exit 1
fi
echo "OK: no blocking findings."
