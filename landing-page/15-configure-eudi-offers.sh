#!/bin/sh
set -eu

target=${EUDI_API_TARGET:-}
if [ -z "$target" ]; then
  cat > /etc/nginx/eudi-offers-location.conf <<'EOF'
location = /eudi-offers.json {
    return 503;
}
EOF
  exit 0
fi

if ! printf '%s' "$target" | grep -Eq '^https?://[A-Za-z0-9._:-]+$'; then
  echo "EUDI_API_TARGET must be an HTTP(S) origin without a path" >&2
  exit 1
fi

cat > /etc/nginx/eudi-offers-location.conf <<EOF
location = /eudi-offers.json {
    proxy_pass ${target}/eudi-offers.json;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_cache off;
}
EOF
