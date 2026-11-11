#!/bin/sh
set -eu

escape_json_string() {
  printf '%s' "$1" |
    sed 's/\\/\\\\/g; s/"/\\"/g'
}

escaped_eudi_public_url="$(escape_json_string "${EUDI_PUBLIC_URL:-${VITE_EUDI_PUBLIC_URL:-}}")"
escaped_jaeger_public_url="$(escape_json_string "${JAEGER_PUBLIC_URL:-${VITE_JAEGER_PUBLIC_URL:-}}")"
escaped_grafana_public_url="$(escape_json_string "${GRAFANA_PUBLIC_URL:-${VITE_GRAFANA_PUBLIC_URL:-}}")"
escaped_own_issuer_public_url="$(escape_json_string "${OWN_ISSUER_PUBLIC_URL:-${VITE_OWN_ISSUER_PUBLIC_URL:-}}")"

printf 'window.__GBO_RUNTIME_CONFIG__ = {"eudiPublicUrl":"%s","jaegerPublicUrl":"%s","grafanaPublicUrl":"%s","ownIssuerPublicUrl":"%s"};\n' \
  "$escaped_eudi_public_url" \
  "$escaped_jaeger_public_url" \
  "$escaped_grafana_public_url" \
  "$escaped_own_issuer_public_url" \
  > /usr/share/nginx/html/runtime-config.js
