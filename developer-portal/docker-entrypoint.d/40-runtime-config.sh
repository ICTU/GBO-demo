#!/bin/sh
set -eu

escaped_eudi_public_url="$(
  printf '%s' "${EUDI_PUBLIC_URL:-${VITE_EUDI_PUBLIC_URL:-}}" |
    sed 's/\\/\\\\/g; s/"/\\"/g'
)"
escaped_eudi_client_id="$(
  printf '%s' "${EUDI_CLIENT_ID:-${VITE_EUDI_CLIENT_ID:-}}" |
    sed 's/\\/\\\\/g; s/"/\\"/g'
)"

printf 'window.__GBO_RUNTIME_CONFIG__ = {"eudiPublicUrl":"%s","eudiClientId":"%s"};\n' \
  "$escaped_eudi_public_url" \
  "$escaped_eudi_client_id" \
  > /usr/share/nginx/html/runtime-config.js
