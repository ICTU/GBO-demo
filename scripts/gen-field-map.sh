#!/usr/bin/env bash
# Regenerates services/openftv-pdp/field-map.json from the GraphQL SDLs
# in schemas/pdp-mirror. The OpenFTV GraphQL request-mapper uses this
# map for type-qualified field naming (no full SDL at runtime). Run after
# changing the schemas. CI checks freshness.
#
# The SDLs must NOT live under policies/: the OpenFTV PAP loads every
# file in PDP_POLICIES_STORE as a Rego module, and one unparsable module
# makes OPA keep its previous compiler — silently dropping every policy
# that loads after it in walk order. See CHANGELOG.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=services/openftv-pdp/field-map.json
docker run --rm -v "$PWD:/w" -w /w golang:1.25-alpine \
  sh -c "cd scripts/gen-field-map && go mod tidy >/dev/null 2>&1 && go run . /w/schemas/pdp-mirror /w/$OUT"
