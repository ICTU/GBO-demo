#!/usr/bin/env bash

set -euo pipefail

base_ref="${1:-}"
head_ref="${2:-HEAD}"
output_file="${GITHUB_OUTPUT:-}"

go_services='[]'
frontends='[]'
docker_services='[]'

append_name() {
  local current="$1"
  local name="$2"

  jq -c --arg name "${name}" '. + [$name]' <<<"${current}"
}

path_changed() {
  local path="$1"

  [[ -z "${base_ref}" ]] ||
    ! git diff --quiet "${base_ref}" "${head_ref}" -- "${path}"
}

run_all=false
if [[ -z "${base_ref}" ]] ||
  ! git diff --quiet \
    "${base_ref}" \
    "${head_ref}" \
    -- \
    .github/workflows/ci.yml \
    .github/scripts/ci-change-plan.sh; then
  run_all=true
fi

while read -r service; do
  if [[ "${run_all}" == "true" ]] ||
    path_changed "services/${service}" ||
    path_changed ".golangci.yml"; then
    go_services="$(append_name "${go_services}" "${service}")"
  fi
done <<'GO_SERVICES'
additional-claims-service
bron-sidecar
bsnk-mock
consent-portal-backend
consent-register
dev-portal-backend
dienstverlener-backend
eudi-adapter
graphql-server
pdp-service
sector-pip
GO_SERVICES

while read -r app; do
  if [[ "${run_all}" == "true" ]] || path_changed "${app}"; then
    frontends="$(append_name "${frontends}" "${app}")"
  fi
done <<'FRONTENDS'
developer-portal
dienstverlener-mock
toestemmingsportaal-frontend
FRONTENDS

while read -r service; do
  if [[ "${run_all}" == "true" ]] || path_changed "services/${service}"; then
    docker_services="$(append_name "${docker_services}" "${service}")"
  fi
done <<'DOCKER_SERVICES'
additional-claims-service
eudi-adapter
graphql-server
pdp-service
sector-pip
DOCKER_SERVICES

rego=false
if [[ "${run_all}" == "true" ]] || path_changed "policies"; then
  rego=true
fi

chart=false
if [[ "${run_all}" == "true" ]] || path_changed "deploy/helm/gbo-app"; then
  chart=true
fi

result="$(
  printf 'go=%s\n' "${go_services}"
  printf 'go_count=%s\n' "$(jq 'length' <<<"${go_services}")"
  printf 'frontends=%s\n' "${frontends}"
  printf 'frontend_count=%s\n' "$(jq 'length' <<<"${frontends}")"
  printf 'docker=%s\n' "${docker_services}"
  printf 'docker_count=%s\n' "$(jq 'length' <<<"${docker_services}")"
  printf 'rego=%s\n' "${rego}"
  printf 'chart=%s\n' "${chart}"
)"

if [[ -n "${output_file}" ]]; then
  printf '%s\n' "${result}" >>"${output_file}"
else
  printf '%s\n' "${result}"
fi
