#!/usr/bin/env bash
# Generates cert-tuples for the Hypotheekverlener-mock-org.
#
# Analogous to bootstrap-edi-issuer.sh / bootstrap-bd-mock.sh:
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

HV_ORG="../orgs/hypotheekverlener-mock/pki/org"
HV_INT="../orgs/hypotheekverlener-mock/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${HV_ORG}/hypotheekverlener.pem" && -f "${HV_INT}/internal-cert.pem" ]]; then
    echo "Hypotheekverlener-mock certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating Hypotheekverlener-mock cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: hypotheekverlener (OIN=99999999900000000300, hosts=hv-manager,hv-outway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO Hypotheekverlener-mock/OU=TEST/CN=hv-manager/serialNumber=99999999900000000300" \
            -addext "subjectAltName=DNS:hv-manager,DNS:hv-outway" \
            -keyout "${OUT}/hypotheekverlener-key.pem" \
            -out "${OUT}/hypotheekverlener.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/hypotheekverlener.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/hypotheekverlener.pem"

        if [[ ! -s "${OUT}/hypotheekverlener.pem" ]]; then
            echo "FAIL: no cert for hypotheekverlener" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/hypotheekverlener.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/hypotheekverlener.pem" >/dev/null

        mkdir -p /work/orgs/hypotheekverlener-mock/pki/org
        mv "${OUT}/hypotheekverlener-key.pem" /work/orgs/hypotheekverlener-mock/pki/org/
        mv "${OUT}/hypotheekverlener.pem"     /work/orgs/hypotheekverlener-mock/pki/org/
        cp pki/ca/root.pem                    /work/orgs/hypotheekverlener-mock/pki/org/
        chmod 600 /work/orgs/hypotheekverlener-mock/pki/org/hypotheekverlener-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for hypotheekverlener-mock --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/hypotheekverlener-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/hypotheekverlener-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/hypotheekverlener-mock/pki/internal
        mv intermediate_ca.pem      /work/orgs/hypotheekverlener-mock/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/hypotheekverlener-mock/pki/internal/
        mv internal-cert.pem        /work/orgs/hypotheekverlener-mock/pki/internal/
        mv internal-cert-key.pem    /work/orgs/hypotheekverlener-mock/pki/internal/
        chmod 600 /work/orgs/hypotheekverlener-mock/pki/internal/*-key.pem
    '

# OpenFSC containers run as appuser (uid/gid 1001) and must be able to
# read the key material.
docker run --rm -v "$(pwd)/..:/work" "${IMAGE_TAG}" \
    chown -R 1001:1001 /work/orgs/hypotheekverlener-mock/pki

echo
echo ">>> Done."
ls -la ../orgs/hypotheekverlener-mock/pki/org/ ../orgs/hypotheekverlener-mock/pki/internal/
