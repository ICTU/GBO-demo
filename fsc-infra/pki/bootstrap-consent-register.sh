#!/usr/bin/env bash
# Generates cert-tuples for the consent register's own FSC peer (#383).
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
NETWORK="${FSC_INFRA_NETWORK:-fsc-infra_default}"

ORG_DIR="../orgs/consent-register/pki/org"
INT_DIR="../orgs/consent-register/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${ORG_DIR}/consent-register.pem" && -f "${INT_DIR}/internal-cert.pem" ]]; then
    echo "Consent-register certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating Consent-register cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: consent-register (OIN=99999999900000000700, hosts=cr-manager,cr-inway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO Toestemmingsregister/OU=TEST/CN=cr-manager/serialNumber=99999999900000000700" \
            -addext "subjectAltName=DNS:cr-manager,DNS:cr-inway" \
            -keyout "${OUT}/consent-register-key.pem" \
            -out "${OUT}/consent-register.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/consent-register.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/consent-register.pem"

        if [[ ! -s "${OUT}/consent-register.pem" ]]; then
            echo "FAIL: no cert for consent-register" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/consent-register.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/consent-register.pem" >/dev/null

        mkdir -p /work/orgs/consent-register/pki/org
        mv "${OUT}/consent-register-key.pem" /work/orgs/consent-register/pki/org/
        mv "${OUT}/consent-register.pem"     /work/orgs/consent-register/pki/org/
        cp pki/ca/root.pem                    /work/orgs/consent-register/pki/org/
        chmod 600 /work/orgs/consent-register/pki/org/consent-register-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for consent-register --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/consent-register-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/consent-register-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/consent-register/pki/internal
        mv intermediate_ca.pem      /work/orgs/consent-register/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/consent-register/pki/internal/
        mv internal-cert.pem        /work/orgs/consent-register/pki/internal/
        mv internal-cert-key.pem    /work/orgs/consent-register/pki/internal/
        chmod 600 /work/orgs/consent-register/pki/internal/*-key.pem
    '

# OpenFSC containers run as appuser (uid/gid 1001) and must be able to
# read the key material.
docker run --rm -v "$(pwd)/..:/work" "${IMAGE_TAG}" \
    chown -R 1001:1001 /work/orgs/consent-register/pki

echo
echo ">>> Done."
ls -la ../orgs/consent-register/pki/org/ ../orgs/consent-register/pki/internal/
