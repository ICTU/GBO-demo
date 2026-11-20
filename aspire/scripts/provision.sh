#!/usr/bin/env bash
# One-off provisioning on the host, for the EUDI, ASI and credential-issuer
# setups. These were Makefile targets; the steps that every run needs (the
# development certificates of the demo sources, the QTSP issuance
# certificates, the ASIP seal and QTSP certificates) run from the AppHost.
#
#   aspire/scripts/provision.sh <step> [NAME=value ...]
#
# Steps:
#   trust-list-signer                  LoTE scheme root and list signer
#   publish-trust-lists                sign and publish the four LoTE lists      (EUDI_TRUST_PUBLIC_URL)
#   development-registrar-root         registrar root for WRPRCs, created once
#   wallet-registration                WRPAC + WRPRC of one source               (SOURCE_ID, KVK_NUMBER the first time)
#   test-pid                           test PID provider of the own issuer        (ISSUER_PUBLIC_URL)
#   qtsp-wallet-registration           the reference QTSP as provider of the own issuer
#   asi-wallet-verifier                NL Wallet verifier configuration for the ASI route
#   asi-eudi-wallet-verifier           EUDI wallet verifier configuration         (EUDI_TRUST_PUBLIC_URL)
#
# Signing runs here, with go run, so no CA or signer key ever enters a
# container. Settings come from NAME=value arguments or the environment;
# the defaults match apphost.go (Gbo:SecretsDir and friends).
set -euo pipefail

cd "$(dirname "$0")/../.."
repo="$PWD"

step="${1:-}"
[ -n "$step" ] || { sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
shift
for assignment in "$@"; do
  case "$assignment" in
    *=*) export "${assignment?}" ;;
    *) echo "ERROR: expected NAME=value, got $assignment" >&2; exit 2 ;;
  esac
done

SECRETS_DIR="${ONBOARDING_SECRETS_DIR:-$repo/.local/secrets}"
PUBLIC_CERTIFICATES_DIR="${ONBOARDING_PUBLIC_CERTIFICATES_DIR:-$repo/.local/public-certificates}"
TRUST_PUBLISH_DIR="${EUDI_TRUST_PUBLISH_DIR:-$repo/.local/public-trust}"
LOTE_SIGNER_DIR="${GBO_LOTE_SIGNER_DIR:-$repo/.local/lote-signer}"
LOTE_SIGNER_ROOT_FILE="${GBO_LOTE_SIGNER_ROOT_FILE:-$PUBLIC_CERTIFICATES_DIR/development-ca/lote-ca-cert.pem}"
PID_TRUST_LIST_URL="${GBO_PID_TRUST_LIST_URL:-${EUDI_TRUST_PUBLIC_URL:+${EUDI_TRUST_PUBLIC_URL%/}/lote/PIDProviders.jwt}}"
DEVELOPMENT_CA_DIR="$SECRETS_DIR/development-ca"
PUBLIC_HOST="${GBO_PUBLIC_HOST:-localhost}"
PUBLIC_SCHEME="${GBO_PUBLIC_SCHEME:-http}"

eudi_adapter() { (cd "$repo/services/eudi-adapter" && go run . "$@"); }

require() {
  for name in "$@"; do
    [ -n "${!name:-}" ] || { echo "ERROR: set $name" >&2; exit 1; }
  done
}

require_development_cas() {
  for f in issuer-ca-key.pem issuer-ca-cert.pem reader-ca-key.pem reader-ca-cert.pem; do
    [ -f "$DEVELOPMENT_CA_DIR/$f" ] || { echo "ERROR: pre-provisioned EUDI CA file is required: $DEVELOPMENT_CA_DIR/$f" >&2; exit 1; }
  done
}

# The Source Registry's Postgres under the running AppHost, from the host.
source_registry_url() {
  local id port password
  id=$(docker ps -q --filter label=com.docker.compose.project=gbo-demo-aspire \
    --filter label=com.docker.compose.service=postgres-eudi | head -1)
  [ -n "$id" ] || { echo "ERROR: postgres-eudi is not running; start the AppHost with an eudi profile" >&2; exit 1; }
  port=$(docker port "$id" 5432/tcp | head -1 | sed 's/.*://')
  password=$(cd "$repo/aspire" && aspire secret get Parameters:source-registry-reader-password --non-interactive 2>/dev/null | tail -1)
  [ -n "$password" ] || { echo "ERROR: no source-registry-reader-password in the AppHost secrets" >&2; exit 1; }
  echo "postgres://source_registry_reader:${password}@localhost:${port}/source_registry"
}

case "$step" in
  trust-list-signer)
    # Sign only on the host. Neither the scheme root nor the list signing key
    # belongs in the secrets directory or in either public projection.
    eudi_adapter provision-trust-list-signer \
      --root-dir "$LOTE_SIGNER_DIR" --secrets-dir "$SECRETS_DIR" \
      --public-certificates-dir "$PUBLIC_CERTIFICATES_DIR" --publish-dir "$TRUST_PUBLISH_DIR"
    ;;
  publish-trust-lists)
    # A failed fetch or signature check keeps all four previous lists intact.
    require EUDI_TRUST_PUBLIC_URL
    # shellcheck disable=SC2086 # GBO_LOTE_PUBLISH_FLAGS is a list of flags.
    eudi_adapter publish-trust-lists \
      --root-dir "$LOTE_SIGNER_DIR" --secrets-dir "$SECRETS_DIR" \
      --public-certificates-dir "$PUBLIC_CERTIFICATES_DIR" --publish-dir "$TRUST_PUBLISH_DIR" \
      --trust-public-url "$EUDI_TRUST_PUBLIC_URL" --nl-config-key-file "${GBO_LOTE_NL_CONFIG_KEY_FILE:-}" \
      --trust-nl-https="${GBO_LOTE_TRUST_NL_HTTPS:-true}" ${GBO_LOTE_PUBLISH_FLAGS:-}
    ;;
  development-registrar-root)
    # The one trust anchor this setup creates: on request, once, and never
    # replaced, since wallets trust it by its key.
    eudi_adapter create-development-registrar-root \
      --secrets-dir "$SECRETS_DIR" --public-certificates-dir "$PUBLIC_CERTIFICATES_DIR"
    ;;
  wallet-registration)
    # Exports the source from the running Source Registry, then signs its
    # access and registration certificates here.
    require_development_cas
    require SOURCE_ID
    mkdir -p "$TRUST_PUBLISH_DIR"
    url=$(source_registry_url)
    (cd "$repo/services/eudi-adapter" && \
      SOURCE_REGISTRY_DATABASE_URL="$url" SOURCE_REGISTRY_SCHEMA=source_registry \
        go run . export-wallet-registration-source --source-id "$SOURCE_ID") \
      | eudi_adapter provision-wallet-registration --source-file - \
          --secrets-dir "$SECRETS_DIR" --publish-dir "$TRUST_PUBLISH_DIR" --kvk-number "${KVK_NUMBER:-}"
    ;;
  test-pid)
    require_development_cas
    require ISSUER_PUBLIC_URL
    mkdir -p "$TRUST_PUBLISH_DIR"
    eudi_adapter provision-test-pid \
      --secrets-dir "$SECRETS_DIR" --publish-dir "$TRUST_PUBLISH_DIR" \
      --public-certificates-dir "$PUBLIC_CERTIFICATES_DIR" --root-dir "${TEST_PID_ROOT_DIR:-$repo/.local/test-pid-root}" \
      --issuer-public-url "$ISSUER_PUBLIC_URL"
    ;;
  qtsp-wallet-registration)
    require_development_cas
    mkdir -p "$TRUST_PUBLISH_DIR"
    eudi_adapter provision-qtsp-wallet-registration \
      --secrets-dir "$SECRETS_DIR" --publish-dir "$TRUST_PUBLISH_DIR"
    ;;
  asi-wallet-verifier)
    # Reader certificates of the asi-login and qtsp-login usecases, PID roots
    # from the verified central LoTE, and the API key of the verifier's
    # internal API. Publish the central HTTPS list first. Rerun when
    # ASI_WALLET_VERIFIER_PUBLIC_URL changes, then restart the ASI resources.
    require_development_cas
    require PID_TRUST_LIST_URL
    dir="$SECRETS_DIR/asi-wallet-verifier"
    eudi_adapter export-pid-trust \
      --url "$PID_TRUST_LIST_URL" --signer-root "$LOTE_SIGNER_ROOT_FILE" \
      --out-file "$dir/pid-issuer-anchors.txt"
    (cd "$repo/services/asi-authorization-server" && go run . provision-wallet-verifier \
      --ca-key "$DEVELOPMENT_CA_DIR/reader-ca-key.pem" \
      --ca-cert "$DEVELOPMENT_CA_DIR/reader-ca-cert.pem" \
      --pid-issuer-anchors "$dir/pid-issuer-anchors.txt" \
      --public-url "${ASI_WALLET_VERIFIER_PUBLIC_URL:-http://localhost:9428}" \
      --universal-link-base "${ASI_WALLET_UL_BASE:-https://app.preproductie.wallet.edi.bzk.nl/deeplink/}" \
      --request-origin "${ASI_AUTHORIZATION_SERVER_PUBLIC_URL:-$PUBLIC_SCHEME://$PUBLIC_HOST:9425}" \
      --qtsp-request-origin "${QTSP_PUBLIC_URL:-$PUBLIC_SCHEME://$PUBLIC_HOST:9427}" \
      --out-dir "$dir" \
      --api-key-file "$SECRETS_DIR/asi-authorization-server-wallet/api-key")
    ;;
  asi-eudi-wallet-verifier)
    # The authorization server registered as relying party like a source,
    # the PID issuers it trusts, and the API key it uses. Rerun when
    # ASI_EUDI_WALLET_VERIFIER_PUBLIC_URL or ASI_AUTHORIZATION_SERVER_PUBLIC_URL
    # changes, then restart the ASI resources.
    require_development_cas
    require EUDI_TRUST_PUBLIC_URL
    mkdir -p "$TRUST_PUBLISH_DIR"
    eudi_adapter provision-asi-eudi-wallet-verifier \
      --secrets-dir "$SECRETS_DIR" --publish-dir "$TRUST_PUBLISH_DIR" \
      --pid-trust-list-url "$PID_TRUST_LIST_URL" --lote-signer-root "$LOTE_SIGNER_ROOT_FILE" \
      --public-url "${ASI_EUDI_WALLET_VERIFIER_PUBLIC_URL:-http://localhost:9432}" \
      --authorization-server-url "${ASI_AUTHORIZATION_SERVER_PUBLIC_URL:-$PUBLIC_SCHEME://$PUBLIC_HOST:9425}"
    ;;
  *)
    echo "ERROR: unknown step $step" >&2
    exit 2
    ;;
esac
