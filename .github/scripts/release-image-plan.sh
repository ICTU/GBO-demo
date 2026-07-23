#!/usr/bin/env bash

set -euo pipefail

base_ref="${1:-}"
head_ref="${2:-HEAD}"
output_file="${GITHUB_OUTPUT:-}"

changed='[]'
unchanged='[]'

append_service() {
  local bucket="$1"
  local service="$2"
  local context="$3"
  local dockerfile="$4"
  local item

  item="$(
    jq -nc \
      --arg service "${service}" \
      --arg context "${context}" \
      --arg dockerfile "${dockerfile}" \
      '{service: $service, context: $context, dockerfile: $dockerfile}'
  )"

  if [[ "${bucket}" == "changed" ]]; then
    changed="$(jq -c --argjson item "${item}" '. + [$item]' <<<"${changed}")"
  else
    unchanged="$(jq -c --argjson item "${item}" '. + [$item]' <<<"${unchanged}")"
  fi
}

path_changed() {
  local path="$1"

  [[ -z "${base_ref}" ]] ||
    ! git diff --quiet "${base_ref}" "${head_ref}" -- "${path}"
}

while IFS='|' read -r service context dockerfile change_path; do
  if path_changed "${change_path}"; then
    append_service changed "${service}" "${context}" "${dockerfile}"
  else
    append_service unchanged "${service}" "${context}" "${dockerfile}"
  fi
done <<'SERVICES'
additional-claims-service|./services/additional-claims-service|./services/additional-claims-service/Dockerfile|services/additional-claims-service
bron-sidecar|./services/bron-sidecar|./services/bron-sidecar/Dockerfile|services/bron-sidecar
bsnk-mock|./services/bsnk-mock|./services/bsnk-mock/Dockerfile|services/bsnk-mock
consent-portal-backend|./services/consent-portal-backend|./services/consent-portal-backend/Dockerfile|services/consent-portal-backend
consent-register|./services/consent-register|./services/consent-register/Dockerfile|services/consent-register
dev-portal-backend|./services/dev-portal-backend|./services/dev-portal-backend/Dockerfile|services/dev-portal-backend
developer-portal|./developer-portal|./developer-portal/Dockerfile|developer-portal
dienstverlener-backend|./services/dienstverlener-backend|./services/dienstverlener-backend/Dockerfile|services/dienstverlener-backend
dienstverlener-mock|./dienstverlener-mock|./dienstverlener-mock/Dockerfile|dienstverlener-mock
eudi-adapter|./services/eudi-adapter|./services/eudi-adapter/Dockerfile|services/eudi-adapter
graphql-server|./services/graphql-server|./services/graphql-server/Dockerfile|services/graphql-server
pdp-service|./services/pdp-service|./services/pdp-service/Dockerfile|services/pdp-service
sector-pip|./services/sector-pip|./services/sector-pip/Dockerfile|services/sector-pip
toestemmingsportaal-frontend|./toestemmingsportaal-frontend|./toestemmingsportaal-frontend/Dockerfile|toestemmingsportaal-frontend
SERVICES

eudi_changed=false
if [[ -z "${base_ref}" ]] ||
  ! git diff --quiet \
    "${base_ref}" \
    "${head_ref}" \
    -- \
    vendor/nl-wallet \
    services/eudi-issuance-server/Dockerfile; then
  eudi_changed=true
fi

result="$(
  printf 'changed=%s\n' "${changed}"
  printf 'unchanged=%s\n' "${unchanged}"
  printf 'changed_count=%s\n' "$(jq 'length' <<<"${changed}")"
  printf 'unchanged_count=%s\n' "$(jq 'length' <<<"${unchanged}")"
  printf 'eudi_changed=%s\n' "${eudi_changed}"
)"

if [[ -n "${output_file}" ]]; then
  printf '%s\n' "${result}" >>"${output_file}"
else
  printf '%s\n' "${result}"
fi
