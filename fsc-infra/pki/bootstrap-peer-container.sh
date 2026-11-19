#!/usr/bin/env bash
# Generates the certificates of one FSC party inside the pki-tools container.
# Run by bootstrap-peer.sh, which mounts fsc-infra on /work and passes the
# fields of the party's row in peers.txt as environment variables: NAME, DIR,
# OIN, ORG, HOSTS, INTERNAL_CA, INTERNAL_ORG and INTERNAL_HOSTS, plus DO_GROUP
# (1: also the group certificate, which needs the certportal).

set -o errexit
set -o pipefail
set -o nounset

cd /work
PKI="/work/${DIR}/pki"

if [[ "${DO_GROUP}" == 1 ]]; then
    echo "-- group-cert: ${NAME} (OIN=${OIN}, hosts=${HOSTS}) --"
    OUT=$(mktemp -d)

    # openssl requires an adjusted config to declare serialNumber as a DN
    # attribute.
    echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

    SAN_LIST=""
    for H in ${HOSTS//,/ }; do
        SAN_LIST="${SAN_LIST}DNS:${H},"
    done

    openssl req -new -nodes -sha256 -newkey rsa:4096 \
        -subj "/C=NL/O=${ORG}/OU=TEST/CN=${HOSTS%%,*}/serialNumber=${OIN}" \
        -addext "subjectAltName=${SAN_LIST%,}" \
        -keyout "${OUT}/${NAME}-key.pem" \
        -out "${OUT}/${NAME}.csr" 2>/dev/null

    CSR_JSON=$(jq -sR . < "${OUT}/${NAME}.csr")
    curl -fsS -X POST http://certportal:8080/api/request_certificate \
        -H "Content-Type: application/json" \
        -d "{\"csr\":${CSR_JSON}}" \
    | jq -r ".certificate" > "${OUT}/${NAME}.pem"

    if [[ ! -s "${OUT}/${NAME}.pem" ]]; then
        echo "FAIL: no cert for ${NAME}" >&2
        exit 1
    fi

    # Sanity: subject log + chain check
    openssl x509 -in "${OUT}/${NAME}.pem" -noout -subject
    openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/${NAME}.pem" >/dev/null

    mkdir -p "${PKI}/org"
    mv "${OUT}/${NAME}-key.pem" "${PKI}/org/"
    mv "${OUT}/${NAME}.pem"     "${PKI}/org/"
    cp pki/ca/root.pem          "${PKI}/org/"
    chmod 600 "${PKI}/org/${NAME}-key.pem"
fi

if [[ "${INTERNAL_CA}" != "-" ]]; then
    echo "-- internal-CA + internal-cert for ${NAME} --"
    TMP=$(mktemp -d)
    cd "${TMP}"
    jq -n --arg cn "GBO-DEMO Internal CA (${INTERNAL_CA})" --arg o "${INTERNAL_ORG}" \
        "{CN: \$cn, key: {algo: \"rsa\", size: 4096}, names: [{C: \"NL\", O: \$o}], ca: {expiry: \"43800h\"}}" \
        > internal-ca.json
    jq -n --arg cn "${NAME}-internal" --arg o "${INTERNAL_ORG}" --arg hosts "${INTERNAL_HOSTS}" \
        "{CN: \$cn, hosts: (\$hosts | split(\",\")), key: {algo: \"rsa\", size: 4096}, names: [{C: \"NL\", O: \$o}]}" \
        > internal-cert.json
    cfssl gencert -initca internal-ca.json | cfssljson -bare intermediate_ca
    cfssl gencert \
        -ca=intermediate_ca.pem \
        -ca-key=intermediate_ca-key.pem \
        -config=/work/pki/cfssl-signing-config.json \
        -profile=server \
        internal-cert.json \
    | cfssljson -bare internal-cert

    mkdir -p "${PKI}/internal"
    mv intermediate_ca.pem      "${PKI}/internal/"
    mv intermediate_ca-key.pem  "${PKI}/internal/"
    mv internal-cert.pem        "${PKI}/internal/"
    mv internal-cert-key.pem    "${PKI}/internal/"
    chmod 600 "${PKI}/internal/"*-key.pem
fi

# OpenFSC containers run as appuser (uid/gid 1001).
if [[ "${DO_GROUP}" == 1 ]]; then
    chown -R 1001:1001 "${PKI}"
elif [[ "${INTERNAL_CA}" != "-" ]]; then
    chown -R 1001:1001 "${PKI}/internal"
fi
