#!/usr/bin/env bash
# Regenerates policies/dvtp/gbo/graphql_schemas.rego: the digest of the
# GraphQL schema the PDP loads per FSC service, as named in
# schemas/pdp-mirror/services.json. The schemas travel with the PDP image,
# so the policy bundle pins them. Run after changing a schema or the
# manifest. CI checks freshness.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=policies/dvtp/gbo/graphql_schemas.rego
docker run --rm -v "$PWD:/w" -w /w golang:1.25-alpine \
  sh -c "cd services/ftv-graphql-mapper && go run ./cmd/ftv-graphql-pins /w/schemas/pdp-mirror /w/$OUT"
