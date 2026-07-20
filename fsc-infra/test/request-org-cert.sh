#!/usr/bin/env bash
# Verification script: can our certportal sign a CSR?
#
# Generates a test CSR, POSTs it to our local certportal
# (http://localhost:8091, or via the docker-network), and verifies the
# cert against the root-CA + intermediate-CA in pki/ca/.
#
# Uses the gbo-demo/pki-tools:local image (openssl+curl+jq inside), no
# host installations required.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")/.."

MANAGER_DOMAIN="${1:-manager.test-org.local}"
INWAY_DOMAIN="${2:-inway.test-org.local}"
OIN="${3:-99999999900000000001}"
CERTPORTAL_URL="${CERTPORTAL_URL:-http://certportal:8080}"

IMAGE_TAG="gbo-demo/pki-tools:local"
NETWORK="fsc-infra_default"

if [[ ! -f pki/ca/root.pem ]]; then
    echo "ERROR: pki/ca/root.pem is missing. Run first: bash pki/generate-root-ca.sh"
    exit 1
fi

echo ">>> Test CSR + certportal request:"
echo "    Manager domain : ${MANAGER_DOMAIN}"
echo "    Inway domain   : ${INWAY_DOMAIN}"
echo "    OIN            : ${OIN}"

docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/pki:/pki:ro" \
    -w /tmp \
    -e MANAGER_DOMAIN="${MANAGER_DOMAIN}" \
    -e INWAY_DOMAIN="${INWAY_DOMAIN}" \
    -e OIN="${OIN}" \
    -e CERTPORTAL_URL="${CERTPORTAL_URL}" \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail

        # openssl requires an adjusted config for serialNumber
        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        echo "-- Generating CSR --"
        openssl req -new -nodes -sha256 -newkey rsa:2048 \
            -subj "/C=NL/O=GBO-DEMO Test Org/OU=TEST/CN=${MANAGER_DOMAIN}/serialNumber=${OIN}" \
            -addext "subjectAltName=DNS:${MANAGER_DOMAIN},DNS:${INWAY_DOMAIN}" \
            -keyout org.key -out org.csr 2>/dev/null

        echo "-- Posting CSR to certportal (${CERTPORTAL_URL}) --"
        CSR_JSON=$(jq -sR . < org.csr)
        curl -fsS -X POST "${CERTPORTAL_URL}/api/request_certificate" \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > org.crt

        if [[ ! -s org.crt ]]; then
            echo "FAIL: no cert in response"
            exit 1
        fi

        echo "-- Chain verify --"
        openssl verify -CAfile /pki/ca/root.pem -untrusted /pki/ca/intermediate.pem org.crt

        echo "-- Cert-subject --"
        openssl x509 -in org.crt -noout -subject -issuer -dates
    '

echo
echo ">>> OK — certportal works against our root-CA."
