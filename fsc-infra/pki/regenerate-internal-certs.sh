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
            # OpenFSC containers run as appuser (uid/gid 1001), as in the bootstrap scripts.
            chown -R 1001:1001 /work/${ORG_DIR}/pki/internal
        "
}

# name, internal CA config, internal cert config, peer directory
PEERS=(
    "directory-peer      internal-ca.json                     directory-internal-cert.json           directory-peer"
    "edi-issuer          edi-issuer-internal-ca.json          edi-issuer-internal-cert.json          orgs/edi-issuer"
    "bd-mock             bd-mock-internal-ca.json             bd-mock-internal-cert.json             orgs/belastingdienst-mock"
    "brp-mock            brp-mock-internal-ca.json            brp-mock-internal-cert.json            orgs/brp-mock"
    "hypotheekverlener   hypotheekverlener-internal-ca.json   hypotheekverlener-internal-cert.json   orgs/hypotheekverlener-mock"
    "consent-register    consent-register-internal-ca.json    consent-register-internal-cert.json    orgs/consent-register"
    "gbo-pdp             gbo-pdp-internal-ca.json             gbo-pdp-internal-cert.json             orgs/gbo-pdp"
    "installatieregister installatieregister-internal-ca.json installatieregister-internal-cert.json orgs/installatieregister"
    "integrator          integrator-internal-ca.json          integrator-internal-cert.json          orgs/integrator"
    "asip                asip-internal-ca.json                asip-internal-cert.json                orgs/asip"
)

# Every internal cert config the bootstrap scripts use must be listed above,
# so a new peer cannot be left out of a regeneration.
LISTED=" $(for p in "${PEERS[@]}"; do read -r _ _ cert _ <<<"$p"; printf '%s ' "$cert"; done)"
MISSING=0
for cert in *internal-cert.json; do
    if [[ "$LISTED" != *" ${cert} "* ]]; then
        echo "ERROR: ${cert} has no entry in PEERS in $(basename "$0")" >&2
        MISSING=1
    fi
done
[[ "$MISSING" == 0 ]] || exit 1

for p in "${PEERS[@]}"; do
    # shellcheck disable=SC2086
    _regen $p
done

echo
echo ">>> Done. Restart all FSC containers to load the new internal certs."
