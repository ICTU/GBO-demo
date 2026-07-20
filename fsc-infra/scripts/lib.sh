#!/usr/bin/env bash
# Shared helpers for FSC contract-seed scripts.
# Requires: openssl, curl, jq, python3 on host.

# uuid_v7 — 128-bit UUID v7 (48-bit unix_ts_ms + version + rand_a + variant + rand_b).
# FSC validates strictly on v7 (open-fsc/txlog-api/domain/record/record.go).
# Uses python3; supports 3.9+ (self-implements v7 since uuid.uuid7 is Python 3.14+).
uuid_v7() {
  python3 - <<'PY'
import os, time
b = bytearray(os.urandom(16))
ts = int(time.time() * 1000)
b[0] = (ts >> 40) & 0xFF
b[1] = (ts >> 32) & 0xFF
b[2] = (ts >> 24) & 0xFF
b[3] = (ts >> 16) & 0xFF
b[4] = (ts >> 8) & 0xFF
b[5] = ts & 0xFF
b[6] = (b[6] & 0x0F) | 0x70   # version 7
b[8] = (b[8] & 0x3F) | 0x80   # RFC 4122 variant
h = b.hex()
print(f"{h[0:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:32]}")
PY
}

# now_epoch — Unix epoch seconds (int64). FSC's timestamp schema is
# int64-seconds since epoch (open-fsc/manager/ports/int/rest/api/openapi.yaml
# schemas/timestamp), not RFC 3339 ISO.
now_epoch() {
  date -u +%s
}

# plus_years_epoch <n> — Unix epoch seconds, N years from now.
plus_years_epoch() {
  local years="$1"
  python3 -c "import time; print(int(time.time()) + 365*24*3600*$years)"
}

# pubkey_thumbprint_hex <cert.pem> — SHA-256 hex of the public key (DER).
# FSC's outway identification uses this format (see openapi.yaml
# publicKeyThumbprint schema, "SHA256 thumbprint ... in HEX-encoded format").
pubkey_thumbprint_hex() {
  local cert="$1"
  openssl x509 -in "$cert" -pubkey -noout \
    | openssl pkey -pubin -outform DER 2>/dev/null \
    | openssl dgst -sha256 -hex \
    | awk '{print $2}'
}

# wait_for_url <url> [max_seconds] — poll until URL returns any HTTP status
# under 500. Used to wait for manager/controller to be ready.
wait_for_url() {
  local url="$1"
  local max="${2:-60}"
  local i=0
  while ! curl -s -f -m 2 -o /dev/null "$url" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -ge "$max" ]; then
      echo "timeout waiting for $url" >&2
      return 1
    fi
    sleep 1
  done
}

# mtls_curl <cert> <key> <ca> <url> [extra curl args...] — curl with mTLS
# using the given cert/key against a CA-signed endpoint.
mtls_curl() {
  local cert="$1" key="$2" ca="$3" url="$4"
  shift 4
  curl -s --cert "$cert" --key "$key" --cacert "$ca" "$url" "$@"
}
