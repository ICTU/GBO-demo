#!/bin/sh
set -eu

escaped_eudi_public_url="$(
  printf '%s' "${EUDI_PUBLIC_URL:-${VITE_EUDI_PUBLIC_URL:-}}" |
    sed 's/\\/\\\\/g; s/"/\\"/g'
)"

printf 'window.__GBO_RUNTIME_CONFIG__ = {"eudiPublicUrl":"%s"};\n' \
  "$escaped_eudi_public_url" \
  > /usr/share/nginx/html/runtime-config.js
