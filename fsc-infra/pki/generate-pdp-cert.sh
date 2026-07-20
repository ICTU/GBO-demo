#!/usr/bin/env bash
# Self-signed TLS cert for pdp-service. FSC-Inway's AuthZen plugin
# requires HTTPS with CA-verify for the authorization service. Both
# containers mount this cert:
#   - pdp-service reads it as its TLS server cert
#   - bd-inway reads it as AUTHZEN_CA (self-signed = its own CA)
#
# Cert material lives in services/pdp-service/certs/ (gitignored).

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"
DEST="../../services/pdp-service/certs"

if [[ -f "${DEST}/pdp-service.pem" ]]; then
    echo "PDP-service cert already exists in ${DEST}. Remove to regenerate."
    exit 0
fi

echo ">>> Generating self-signed cert for pdp-service"

docker run --rm \
    -v "$(pwd)/../..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        TMP=$(mktemp -d)
        cd ${TMP}

        # Self-signed leaf cert with SAN=pdp-service. Use openssl (not
        # cfssl -initca; that produces a CA without SAN, which Go TLS
        # will not accept).
        openssl req -x509 -nodes -newkey rsa:4096 -sha256 \
            -days 3650 \
            -subj "/C=NL/O=GBO-DEMO PDP/CN=pdp-service" \
            -addext "subjectAltName=DNS:pdp-service" \
            -keyout pdp-service-key.pem \
            -out pdp-service.pem 2>/dev/null

        mkdir -p /work/services/pdp-service/certs
        mv pdp-service.pem     /work/services/pdp-service/certs/
        mv pdp-service-key.pem /work/services/pdp-service/certs/
        chmod 600              /work/services/pdp-service/certs/pdp-service-key.pem
    '

echo
echo ">>> Done: ${DEST}/pdp-service.pem + pdp-service-key.pem"
