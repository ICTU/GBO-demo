#!/usr/bin/env bash
# Regenerates services/openftv-pdp/field-map.json from the GraphQL SDLs
# in policies/dvtp/schemas. The OpenFTV GraphQL request-mapper uses this
# map for type-qualified field naming (no full SDL at runtime). Run after
# changing the schemas. CI checks freshness.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=services/openftv-pdp/field-map.json
docker run --rm -v "$PWD:/w" -w /w golang:1.25-alpine \
  sh -c "cd scripts/gen-field-map && go mod tidy >/dev/null 2>&1 && go run . /w/policies/dvtp/schemas /w/$OUT"
