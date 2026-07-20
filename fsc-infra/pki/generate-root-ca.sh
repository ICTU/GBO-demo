#!/usr/bin/env bash
# Generates the GBO-DEMO root-CA + intermediate-CA.
#
# Runs everything inside a docker container with cfssl+cfssljson
# available, no host installations needed. Private keys stay in ./ca/
# (gitignored). The fsc-infra compose (cfssl service) then uses the
# intermediate-CA to sign org CSRs.
#
# Idempotent: if ca/root.pem already exists, nothing is overwritten.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"

if [[ -f ca/root.pem && -f ca/intermediate.pem ]]; then
    echo "Root-CA and intermediate-CA already exist in ca/."
    echo "Remove ca/*.pem + ca/*-key.pem first to regenerate."
    exit 0
fi

echo ">>> Building pki-tools image (${IMAGE_TAG})"
docker build -f Dockerfile.pki -t "${IMAGE_TAG}" .

echo
echo ">>> Generating root-CA + intermediate-CA in ca/"
docker run --rm \
    -v "$(pwd):/pki" \
    -w /pki \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        cd ca
        echo "-- root-CA (self-signed) --"
        cfssl gencert -initca ../root-ca.json | cfssljson -bare root

        echo "-- intermediate-CA (CSR) --"
        cfssl gencert -initca ../intermediate-ca.json | cfssljson -bare intermediate

        echo "-- signing intermediate-CA with root-CA (profile: intermediate_ca) --"
        cfssl sign \
            -ca root.pem \
            -ca-key root-key.pem \
            -config ../cfssl-signing-config.json \
            -profile intermediate_ca \
            intermediate.csr \
        | cfssljson -bare intermediate

        chmod 600 root-key.pem intermediate-key.pem
    '

echo
echo ">>> Done. CA material in ca/:"
ls -la ca/*.pem
echo
echo "Chain check:"
docker run --rm -v "$(pwd):/pki" -w /pki "${IMAGE_TAG}" \
    openssl verify -CAfile ca/root.pem ca/intermediate.pem
