#!/usr/bin/env bash
# Regenerates policies/dvtp/gbo/graphql_schemas.rego: the digest of the
# GraphQL schema the PDP loads per FSC service, as named in
# schemas/pdp-mirror/services.json. The schemas travel with the PDP image,
# so the policy bundle pins them. Run after changing a schema or the
# manifest. CI checks freshness.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=policies/dvtp/gbo/graphql_schemas.rego
docker run --rm -v "$PWD:/w" -w /w golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 \
  sh -c "cd services/ftv-graphql-mapper && go run ./cmd/ftv-graphql-pins /w/schemas/pdp-mirror /w/$OUT"
