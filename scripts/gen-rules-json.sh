#!/usr/bin/env bash
# Regenerates services/dev-portal-backend/rules.json from the Rego sources
# (data.dvtp.gbo.rules: rule_id, covers_types, covers_fields, spec per rule).
# The dev-portal serves this file at /rules — the OpenFTV PDP has no data-API
# to evaluate it live. Run after changing policies/dvtp/gbo/rules/*.rego.
set -euo pipefail
cd "$(dirname "$0")/.."

OPA_IMAGE="${OPA_IMAGE:-openpolicyagent/opa:1.9.0-static}"
OUT=services/dev-portal-backend/rules.json

docker run --rm -v "$PWD/policies:/policies:ro" "$OPA_IMAGE" \
  eval --bundle /policies --format json 'data.dvtp.gbo.rules' \
  | python3 -c 'import json,sys; v=json.load(sys.stdin)["result"][0]["expressions"][0]["value"]; print(json.dumps({k:r for k,r in v.items() if isinstance(r,dict) and "rule_id" in r}, indent=2))' \
  > "$OUT"

echo "wrote $OUT"
