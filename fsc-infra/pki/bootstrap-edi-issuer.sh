#!/usr/bin/env bash
# Generates cert-tuples for the EDI-issuer-org.
#
# Analogous to bootstrap-directory-peer.sh:
#   - Group cert via openssl + our certportal (subject.serialNumber = OIN)
#   - Internal-CA + internal-cert self-signed via cfssl
#
# Requires: root-CA (make fsc-ca) + certportal running (make fsc-up).
# Idempotent: skipped if certs already exist.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"
NETWORK="fsc-infra_default"

EDI_ORG="../orgs/edi-issuer/pki/org"
EDI_INT="../orgs/edi-issuer/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${EDI_ORG}/edi-issuer.pem" && -f "${EDI_INT}/internal-cert.pem" ]]; then
    echo "EDI-issuer-org certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating EDI-issuer-org cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: edi-issuer (OIN=99999999900000000100, hosts=edi-manager,edi-outway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO EDI-issuer-org/OU=TEST/CN=edi-manager/serialNumber=99999999900000000100" \
            -addext "subjectAltName=DNS:edi-manager,DNS:edi-outway" \
            -keyout "${OUT}/edi-issuer-key.pem" \
            -out "${OUT}/edi-issuer.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/edi-issuer.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/edi-issuer.pem"

        if [[ ! -s "${OUT}/edi-issuer.pem" ]]; then
            echo "FAIL: no cert for edi-issuer" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/edi-issuer.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/edi-issuer.pem" >/dev/null

        mkdir -p /work/orgs/edi-issuer/pki/org
        mv "${OUT}/edi-issuer-key.pem" /work/orgs/edi-issuer/pki/org/
        mv "${OUT}/edi-issuer.pem"     /work/orgs/edi-issuer/pki/org/
        cp pki/ca/root.pem              /work/orgs/edi-issuer/pki/org/
        chmod 600 /work/orgs/edi-issuer/pki/org/edi-issuer-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for edi-issuer-org --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/edi-issuer-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/edi-issuer-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/edi-issuer/pki/internal
        mv intermediate_ca.pem      /work/orgs/edi-issuer/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/edi-issuer/pki/internal/
        mv internal-cert.pem        /work/orgs/edi-issuer/pki/internal/
        mv internal-cert-key.pem    /work/orgs/edi-issuer/pki/internal/
        chmod 600 /work/orgs/edi-issuer/pki/internal/*-key.pem
    '

echo
echo ">>> Done."
ls -la ../orgs/edi-issuer/pki/org/ ../orgs/edi-issuer/pki/internal/
