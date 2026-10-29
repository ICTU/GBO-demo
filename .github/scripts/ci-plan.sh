#!/usr/bin/env bash

# Build the smallest useful CI matrices for a set of changed paths. Paths are
# read one per line from stdin. Keep dependency fan-out here explicit: a
# sibling module or build input should trigger every consumer that can be
# affected by it.

set -euo pipefail

output_file="${GITHUB_OUTPUT:-}"
changed_files=()
while IFS= read -r changed; do
  [[ -n "${changed}" ]] && changed_files+=("${changed}")
done

matches_any() {
  local changed pattern

  for changed in "${changed_files[@]}"; do
    for pattern in "$@"; do
      # shellcheck disable=SC2254 # patterns are intentionally expanded by case
      case "${changed}" in
        ${pattern}) return 0 ;;
      esac
    done
  done

  return 1
}

append_unique() {
  local json="$1"
  local value="$2"
  jq -c --arg value "${value}" \
    'if index($value) then . else . + [$value] end' <<<"${json}"
}

go='[]'
frontend='[]'
docker='[]'

ci_changed=false
if matches_any '.github/workflows/ci.yml' '.github/scripts/ci-plan.sh'; then
  ci_changed=true
fi

while read -r service; do
  [[ -n "${service}" ]] || continue

  if [[ "${ci_changed}" == true ]] ||
    matches_any "services/${service}/*" '.golangci.yml'; then
    go="$(append_unique "${go}" "${service}")"
  fi
done <<'SERVICES'
asi-authorization-server
asi-discovery-stub
asi-mapping
asi-provider
bron-sidecar
brp-graphql-server
bsnk-mock
consent-portal-backend
consent-register
dev-portal-backend
dienstverlener-backend
dvtp-onboarding-register
eudi-adapter
ftv-graphql-mapper
graphql-server
ldv-client
ldv-logboek
lvg-graphql-server
qtsp-reference
SERVICES

# These services use local replace directives. A change to a sibling module
# must exercise the consumers as well as the module itself.
if matches_any 'services/ldv-client/*'; then
  for service in bron-sidecar brp-graphql-server consent-portal-backend \
    consent-register dienstverlener-backend eudi-adapter graphql-server \
    ldv-logboek lvg-graphql-server; do
    go="$(append_unique "${go}" "${service}")"
  done
fi

if matches_any 'services/ftv-graphql-mapper/*'; then
  go="$(append_unique "${go}" 'bron-sidecar')"
fi

# The source metadata schemas and GBO's attribute catalogue are read by the
# tests of the modules that validate source metadata.
if matches_any 'schemas/gbo-*.schema.json' 'schemas/asi-attributes.json'; then
  for service in asi-mapping eudi-adapter; do
    go="$(append_unique "${go}" "${service}")"
  done
fi

while read -r app; do
  [[ -n "${app}" ]] || continue

  if [[ "${ci_changed}" == true ]] || matches_any "${app}/*"; then
    frontend="$(append_unique "${frontend}" "${app}")"
  fi
done <<'APPS'
developer-portal
landing-page
dienstverlener-mock
keyper-mock
toestemmingsportaal-frontend
APPS

while read -r service; do
  [[ -n "${service}" ]] || continue

  if [[ "${ci_changed}" == true ]] || matches_any "services/${service}/*"; then
    docker="$(append_unique "${docker}" "${service}")"
  fi
done <<'IMAGES'
asi-authorization-server
asi-discovery-stub
asi-provider
dev-portal-backend
dvtp-onboarding-register
eudi-adapter
bron-sidecar
graphql-server
ldv-logboek
lvg-graphql-server
openftv-pdp
qtsp-reference
IMAGES

if matches_any 'services/ldv-client/*'; then
  for service in bron-sidecar eudi-adapter graphql-server ldv-logboek \
    lvg-graphql-server; do
    docker="$(append_unique "${docker}" "${service}")"
  done
fi

if matches_any 'services/ftv-graphql-mapper/*'; then
  docker="$(append_unique "${docker}" 'bron-sidecar')"
  docker="$(append_unique "${docker}" 'openftv-pdp')"
fi

if matches_any 'policies/*'; then
  docker="$(append_unique "${docker}" 'dev-portal-backend')"
  docker="$(append_unique "${docker}" 'openftv-pdp')"
fi

if matches_any 'schemas/pdp-mirror/*' 'docker-compose.yml'; then
  docker="$(append_unique "${docker}" 'openftv-pdp')"
fi

# A root .dockerignore change affects each image built with the repository as
# its context. Service-specific contexts do not read this file.
if matches_any '.dockerignore'; then
  for service in eudi-adapter bron-sidecar graphql-server ldv-logboek \
    lvg-graphql-server openftv-pdp; do
    docker="$(append_unique "${docker}" "${service}")"
  done
fi

source_registry=false
if [[ "${ci_changed}" == true ]] || matches_any 'services/eudi-adapter/*'; then
  source_registry=true
fi

conformance=false
if [[ "${ci_changed}" == true ]] || matches_any \
  'conformance/*' \
  'services/asi-authorization-server/*' \
  'services/asi-provider/*' \
  'services/qtsp-reference/*' \
  '.golangci.yml'; then
  conformance=true
fi

rego=false
if [[ "${ci_changed}" == true ]] || matches_any \
  'policies/*' \
  'schemas/pdp-mirror/*' \
  'services/dev-portal-backend/rules.json' \
  'services/ftv-graphql-mapper/*' \
  'scripts/gen-rules-json.sh' \
  'scripts/gen-graphql-pins.sh'; then
  rego=true
fi

schema_drift=false
if [[ "${ci_changed}" == true ]] || matches_any \
  'schemas/pdp-mirror/*' \
  'services/ftv-graphql-mapper/*' \
  'services/graphql-server/*' \
  'services/brp-graphql-server/*' \
  'services/lvg-graphql-server/*' \
  'scripts/check-schema-drift.sh'; then
  schema_drift=true
fi

image_manifest=false
if [[ "${ci_changed}" == true ]] || matches_any \
  '.github/scripts/image-manifest.sh' \
  '*/Dockerfile'; then
  image_manifest=true
fi

chart=false
if [[ "${ci_changed}" == true ]] || matches_any 'deploy/helm/gbo-app/*'; then
  chart=true
fi

scripts=false
if [[ "${ci_changed}" == true ]] || matches_any \
  'scripts/seed-openftv-manager.py' \
  'scripts/test_seed_openftv_manager.py'; then
  scripts=true
fi

result="$({
  printf 'go=%s\n' "${go}"
  printf 'frontend=%s\n' "${frontend}"
  printf 'docker=%s\n' "${docker}"
  printf 'conformance=%s\n' "${conformance}"
  printf 'source_registry=%s\n' "${source_registry}"
  printf 'rego=%s\n' "${rego}"
  printf 'schema_drift=%s\n' "${schema_drift}"
  printf 'image_manifest=%s\n' "${image_manifest}"
  printf 'chart=%s\n' "${chart}"
  printf 'scripts=%s\n' "${scripts}"
})"

if [[ -n "${output_file}" ]]; then
  printf '%s\n' "${result}" >>"${output_file}"
else
  printf '%s\n' "${result}"
fi
