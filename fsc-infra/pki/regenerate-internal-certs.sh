#!/usr/bin/env bash
# One-off script: regenerates ALL internal-certs (directory-peer +
# orgs) without touching the group certs.
#
# Use after changing *-internal-cert.json (e.g. new SANs). Group-cert
# thumbprints stay identical -> existing contracts stay valid.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"

_regen () {
    local NAME="$1" CA_CFG="$2" CERT_CFG="$3" ORG_DIR="$4"

    echo "-- internal-cert regen: ${NAME} --"

    docker run --rm \
        -v "$(pwd)/..:/work" -w /work \
        "${IMAGE_TAG}" \
        bash -c "
            set -euo pipefail
            TMP=\$(mktemp -d)
            cd \${TMP}
            cfssl gencert -initca /work/pki/${CA_CFG} | cfssljson -bare intermediate_ca
            cfssl gencert \
                -ca=intermediate_ca.pem \
                -ca-key=intermediate_ca-key.pem \
                -config=/work/pki/cfssl-signing-config.json \
                -profile=server \
                /work/pki/${CERT_CFG} \
            | cfssljson -bare internal-cert
            mkdir -p /work/${ORG_DIR}/pki/internal
            mv intermediate_ca.pem      /work/${ORG_DIR}/pki/internal/
            mv intermediate_ca-key.pem  /work/${ORG_DIR}/pki/internal/
            mv internal-cert.pem        /work/${ORG_DIR}/pki/internal/
            mv internal-cert-key.pem    /work/${ORG_DIR}/pki/internal/
            chmod 600 /work/${ORG_DIR}/pki/internal/*-key.pem
        "
}

_regen directory-peer  internal-ca.json               directory-internal-cert.json  directory-peer
_regen edi-issuer      edi-issuer-internal-ca.json    edi-issuer-internal-cert.json orgs/edi-issuer
_regen bd-mock         bd-mock-internal-ca.json       bd-mock-internal-cert.json    orgs/belastingdienst-mock

echo
echo ">>> Done. Restart all FSC containers to load the new internal certs."
