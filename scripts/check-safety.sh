#!/usr/bin/env bash
# Pre-publication safety audit for the demo tree.
#
# Runs before pushing the demo to a public repository. Grep-based checks
# for content that must NOT reach the public internet:
#
#   1. Real OIN's (only demo-numbers like 9999999... are allowed).
#   2. Real names — non-public stakeholders, personal identifiers.
#   3. Internal / VPN-only URLs (*.internal, *.local, tunnel-URLs).
#   4. Secrets — API keys, tokens, private-key material (beyond demo self-signed).
#   5. Mock-BSNs that might collide with real citizens.
#
# Output: findings grouped by category. Exit-1 if any HIGH-severity leak found.
# LOW-severity findings are reported but non-blocking (judgement).
#
# Usage:
#   scripts/check-safety.sh           # audit 05-demo/
#   scripts/check-safety.sh path/     # audit a custom path

set -euo pipefail

TARGET="${1:-05-demo/}"

if [[ ! -d "$TARGET" ]]; then
  echo "check-safety: target directory not found: $TARGET" >&2
  exit 2
fi

# Requires GNU grep for -P (lookaheads).
if ! echo "" | grep -P '' > /dev/null 2>&1; then
  echo "check-safety: this script requires GNU grep (-P support)." >&2
  echo "On macOS: brew install grep && alias grep=ggrep" >&2
  exit 2
fi

INCLUDES=(
  --include='*.go'
  --include='*.rego'
  --include='*.ts'
  --include='*.tsx'
  --include='*.js'
  --include='*.md'
  --include='*.yml'
  --include='*.yaml'
  --include='*.json'
  --include='*.sh'
  --include='Makefile*'
  --include='Dockerfile*'
  --include='*.env*'
  --include='*.graphql'
  --include='*.toml'
)

EXCLUDES=(
  --exclude='check-safety.sh'
  --exclude='package-lock.json'
  --exclude='go.sum'
  --exclude='*.pem'
  --exclude='*.crt'
  # Runtime EUDI-config is git-ignored (carries inline demo signing
  # keys) and never travels to the public repo. Its .example twin
  # with placeholder values is scanned normally.
  --exclude='issuance_server.toml'
  --exclude-dir=node_modules
  --exclude-dir=dist
  --exclude-dir=build
  --exclude-dir=vendor
  --exclude-dir=.git
)

# ── HIGH severity (blocking) ────────────────────────────────────────────

high_hits=0
high_report=""

check_high() {
  local label="$1"
  local pattern="$2"
  local matches
  matches=$(grep -rPn "$pattern" "$TARGET" "${INCLUDES[@]}" "${EXCLUDES[@]}" 2>/dev/null || true)
  if [[ -n "$matches" ]]; then
    local count
    count=$(printf '%s\n' "$matches" | wc -l | tr -d ' ')
    high_hits=$((high_hits + count))
    high_report+=$'\n=== HIGH: '"$label"$' ==='$'\n'"$matches"$'\n'
  fi
}

# Real OIN — 20 digits, and NOT a known demo-OIN.
# Demo-OIN prefixes in this codebase: 99999999 (real-FSC demo orgs),
# 0000000 (synthetic mocks with 7+ leading zeros).
check_high "Non-demo OIN" '(?<![0-9])(?!99999999|0000000)[0-9]{20}(?![0-9])'

# Corporate / VPN-only hostnames — must not ship.
check_high "Corporate/VPN hostnames" '\b[a-zA-Z0-9-]+\.internal\b|\b[a-zA-Z0-9-]+\.corp\b'

# Cloudflared / ngrok tunnels (personal dev-envs).
check_high "Tunnel URLs" 'https?://[a-z0-9-]+\.trycloudflare\.com|https?://[a-z0-9-]+\.ngrok\.io|https?://[a-z0-9-]+\.ngrok-free\.app'

# Bearer-style secrets — API keys / tokens embedded in code (not test-fixture-signed JWTs).
# Heuristic: 20+ char alphanumeric strings with a "key"/"secret"/"token" identifier.
check_high "Hardcoded secret material" '(api[_-]?key|secret|password|passwd|access[_-]?token)\s*[:=]\s*["'\''][A-Za-z0-9_\-+/]{20,}["'\'']'

# Credentials embedded in connection URLs (postgres://user:pass@host, https://user:pass@host).
# Skip matches where the password part is an env-var interpolation (${VAR} or ${VAR:-default})
# — those are the safe form; the literal password lives in .env (git-ignored).
check_high "URL with embedded credentials" '(postgres|postgresql|mysql|mongodb|https?|redis)://[^:/\s"'\''$]+:(?!\$\{)[^@/\s"'\'']+@'

# Inline private-key or certificate material in TOML / JSON / YAML config.
# Nl-wallet-style embedded keys look like: private_key = "MIGHAgEAM..." with
# 40+ base64 chars. Placeholder text (REPLACE_WITH_...) is allowed.
check_high "Inline private_key / certificate in config" '(private_key|certificate)\s*[:=]\s*"(?!REPLACE_)[A-Za-z0-9+/=_-]{40,}"'

# Private-key files outside known demo-PKI locations. Self-signed demo
# material under fsc-infra/**/pki/ and top-level certs/ is fine.
# Implemented via find, not grep.
strays=$(find "$TARGET" -type f \( -name '*.key' -o -name '*key.pem' \) \
  ! -path '*/pki/*' \
  ! -path '*/certs/*' \
  ! -path '*/node_modules/*' 2>/dev/null || true)
if [[ -n "$strays" ]]; then
  count=$(printf '%s\n' "$strays" | wc -l | tr -d ' ')
  high_hits=$((high_hits + count))
  high_report+=$'\n=== HIGH: Private-key material outside fsc-infra/orgs/**/pki/ ==='$'\n'"$strays"$'\n'
fi

# ── LOW severity (reported, non-blocking) ───────────────────────────────

low_report=""

check_low() {
  local label="$1"
  local pattern="$2"
  local matches
  matches=$(grep -rPn "$pattern" "$TARGET" "${INCLUDES[@]}" "${EXCLUDES[@]}" 2>/dev/null || true)
  if [[ -n "$matches" ]]; then
    low_report+=$'\n--- LOW: '"$label"$' ---'$'\n'"$matches"$'\n'
  fi
}

# .local hostnames — usually test fixtures; flag for review.
check_low ".local hostnames (test fixtures likely)" '\b[a-zA-Z0-9-]+\.local\b'

# BSN-shaped 9-digit sequences — most will be mock-BSNs but worth listing so
# you can eyeball whether any real BSN slipped in.
check_low "9-digit numbers (potential BSNs — verify all are mock)" '(?<![0-9])[0-9]{9}(?![0-9])'

# Dutch personal names in comments/strings — heuristic list, expand as needed.
check_low "Common Dutch first names in text" '\b(Jan|Piet|Kees|Marieke|Sanne|Wouter|Anouk|Bart|Femke|Sven|Lars|Jasper|Rob|Hans|Karin|Linda|Maud)\b'

# GitHub @-mentions of collaborators inside markdown / code-comments.
# Restricted to markdown to avoid noise from npm scoped packages (@types/x, @vitejs/x).
low_matches=$(grep -rPn '@[a-zA-Z][a-zA-Z0-9_-]{2,}\b' "$TARGET" --include='*.md' "${EXCLUDES[@]}" 2>/dev/null || true)
if [[ -n "$low_matches" ]]; then
  low_report+=$'\n--- LOW: GitHub @-mentions in markdown (verify all are safe to publish) ---'$'\n'"$low_matches"$'\n'
fi

# ── Report ──────────────────────────────────────────────────────────────

if [[ -n "$high_report" ]]; then
  printf '%s' "$high_report"
fi
if [[ -n "$low_report" ]]; then
  printf '%s' "$low_report"
fi

echo ""
echo "─────────────────────────────────────────────────────────"
if [[ $high_hits -gt 0 ]]; then
  echo "HIGH: $high_hits blocking finding(s) — must resolve before publication."
fi
if [[ -n "$low_report" ]]; then
  echo "LOW:  findings above are informational"
fi
if [[ $high_hits -eq 0 && -z "$low_report" ]]; then
  echo "OK: no safety-audit findings in $TARGET"
fi
echo "─────────────────────────────────────────────────────────"

if [[ $high_hits -gt 0 ]]; then
  exit 1
fi
