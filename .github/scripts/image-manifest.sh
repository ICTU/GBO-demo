#!/usr/bin/env bash

# Emits the full set of images this repository publishes, each with a tag
# derived from its build inputs rather than from a commit or a release.
#
# Identical inputs produce an identical tag, so an image that has already
# been built is found in the registry and promoted to the release version
# instead of being rebuilt. That makes a release a retag of bits that
# were already built and tested on main, and it means correctness no
# longer depends on the previous release tag being complete.

set -euo pipefail
shopt -s nullglob

cd "$(git rev-parse --show-toplevel)"

output_file="${GITHUB_OUTPUT:-}"

images='[]'
covered_paths=()

# Hash every input that can change the built image: the tracked tree
# under the build context, the Dockerfile, and the build parameters
# themselves. git ls-files -s carries mode and blob id per path, so
# content changes and mode changes both land in the hash. Submodules
# appear as their gitlink commit, which is exactly the right identity.
content_tag() {
  local salt="$1"
  shift

  local entries
  entries="$(git ls-files -s -- "$@")"

  if [[ -z "${entries}" ]]; then
    echo "No tracked files under: $*" >&2
    exit 1
  fi

  printf 'ctx-%s\n' "$(
    printf '%s\n%s\n' "${salt}" "${entries}" |
      git hash-object --stdin |
      cut -c1-16
  )"
}

while IFS='|' read -r service context dockerfile paths platforms \
  submodules cache_scope; do
  [[ -n "${service}" ]] || continue

  if [[ ! -f "${dockerfile}" ]]; then
    echo "Image '${service}' refers to ${dockerfile}, which is missing." >&2
    exit 1
  fi

  read -ra hash_paths <<<"${paths}"

  salt="${service}|${context}|${dockerfile}|${platforms}"
  ctx_tag="$(content_tag "${salt}" "${hash_paths[@]}")"

  # Keyed on the Dockerfile's directory, not the context: eudi-issuance-server
  # builds the nl-wallet submodule from a Dockerfile that lives elsewhere.
  dockerfile_dir="${dockerfile%/Dockerfile}"
  covered_paths+=("${dockerfile_dir#./}")

  images="$(
    jq -c \
      --arg service "${service}" \
      --arg context "${context}" \
      --arg dockerfile "${dockerfile}" \
      --arg platforms "${platforms}" \
      --arg submodules "${submodules}" \
      --arg cache_scope "${cache_scope}" \
      --arg ctx_tag "${ctx_tag}" \
      '. + [{
        service: $service,
        context: $context,
        dockerfile: $dockerfile,
        platforms: $platforms,
        submodules: $submodules,
        cache_scope: $cache_scope,
        ctx_tag: $ctx_tag
      }]' <<<"${images}"
  )"
done <<'IMAGES'
additional-claims-service|./services/additional-claims-service|./services/additional-claims-service/Dockerfile|services/additional-claims-service|linux/amd64,linux/arm64|false|additional-claims-service
bron-sidecar|./services/bron-sidecar|./services/bron-sidecar/Dockerfile|services/bron-sidecar|linux/amd64,linux/arm64|false|bron-sidecar
bsnk-mock|./services/bsnk-mock|./services/bsnk-mock/Dockerfile|services/bsnk-mock|linux/amd64,linux/arm64|false|bsnk-mock
consent-portal-backend|./services/consent-portal-backend|./services/consent-portal-backend/Dockerfile|services/consent-portal-backend|linux/amd64,linux/arm64|false|consent-portal-backend
consent-register|./services/consent-register|./services/consent-register/Dockerfile|services/consent-register|linux/amd64,linux/arm64|false|consent-register
dev-portal-backend|./services/dev-portal-backend|./services/dev-portal-backend/Dockerfile|services/dev-portal-backend|linux/amd64,linux/arm64|false|dev-portal-backend
developer-portal|./developer-portal|./developer-portal/Dockerfile|developer-portal|linux/amd64,linux/arm64|false|developer-portal
dienstverlener-backend|./services/dienstverlener-backend|./services/dienstverlener-backend/Dockerfile|services/dienstverlener-backend|linux/amd64,linux/arm64|false|dienstverlener-backend
dienstverlener-mock|./dienstverlener-mock|./dienstverlener-mock/Dockerfile|dienstverlener-mock|linux/amd64,linux/arm64|false|dienstverlener-mock
eudi-adapter|./services/eudi-adapter|./services/eudi-adapter/Dockerfile|services/eudi-adapter|linux/amd64,linux/arm64|false|eudi-adapter
graphql-server|./services/graphql-server|./services/graphql-server/Dockerfile|services/graphql-server|linux/amd64,linux/arm64|false|graphql-server
pdp-service|./services/pdp-service|./services/pdp-service/Dockerfile|services/pdp-service|linux/amd64,linux/arm64|false|pdp-service
sector-pip|./services/sector-pip|./services/sector-pip/Dockerfile|services/sector-pip|linux/amd64,linux/arm64|false|sector-pip
toestemmingsportaal-frontend|./toestemmingsportaal-frontend|./toestemmingsportaal-frontend/Dockerfile|toestemmingsportaal-frontend|linux/amd64,linux/arm64|false|toestemmingsportaal-frontend
eudi-issuance-server|./vendor/nl-wallet|./services/eudi-issuance-server/Dockerfile|vendor/nl-wallet services/eudi-issuance-server/Dockerfile|linux/amd64|recursive|eudi-issuance-server-amd64
IMAGES

# Every image the tree can build is either published above or listed here
# as deliberately unpublished. Without this a new service would be left
# out of every release, silently and permanently.
while read -r unpublished; do
  covered_paths+=("${unpublished}")
done <<'UNPUBLISHED'
services/eudi-demo-issuer
UNPUBLISHED

for dockerfile in */Dockerfile services/*/Dockerfile; do
  image_path="${dockerfile%/Dockerfile}"

  for covered in "${covered_paths[@]}"; do
    if [[ "${covered}" == "${image_path}" ]]; then
      continue 2
    fi
  done

  echo "${image_path} builds an image that nothing publishes." >&2
  echo "Add it to the IMAGES table or to the UNPUBLISHED list." >&2
  exit 1
done

result="$(
  printf 'images=%s\n' "${images}"
  printf 'image_count=%s\n' "$(jq 'length' <<<"${images}")"
)"

if [[ -n "${output_file}" ]]; then
  printf '%s\n' "${result}" >>"${output_file}"
else
  printf '%s\n' "${result}"
fi
