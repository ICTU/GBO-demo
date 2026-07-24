#!/usr/bin/env bash

set -euo pipefail
shopt -s nullglob

cd "$(git rev-parse --show-toplevel)"

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

# Print the directory name of each existing path, sorted. Used to derive
# component lists from the tree instead of hard-coding them, so a newly
# added module or frontend cannot silently skip CI.
dir_names() {
  local path

  for path in "$@"; do
    path="${path%/*}"
    printf '%s\n' "${path##*/}"
  done | sort
}

mapfile -t all_go_services < <(dir_names services/*/go.mod)
mapfile -t all_frontends < <(dir_names ./*/package.json)

# An empty discovery would plan an empty CI run that still reports green,
# so treat it as a planner bug rather than "nothing to do".
if ((${#all_go_services[@]} == 0)) || ((${#all_frontends[@]} == 0)); then
  echo "Component discovery found no Go modules or no frontends." >&2
  echo "Refusing to plan a CI run that would skip everything." >&2
  exit 1
fi

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

for service in "${all_go_services[@]}"; do
  if [[ "${run_all}" == "true" ]] ||
    path_changed "services/${service}" ||
    path_changed ".golangci.yml"; then
    go_services="$(append_name "${go_services}" "${service}")"
  fi
done

for app in "${all_frontends[@]}"; do
  if [[ "${run_all}" == "true" ]] || path_changed "${app}"; then
    frontends="$(append_name "${frontends}" "${app}")"
  fi
done

# Unlike the lists above, the Docker matrix is a deliberate subset of the
# services that carry a Dockerfile, so it stays explicit. Entries are
# checked against the tree to catch renames and deletions.
while read -r service; do
  if [[ ! -f "services/${service}/Dockerfile" ]]; then
    echo "Docker matrix lists '${service}', which has no Dockerfile." >&2
    exit 1
  fi

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
