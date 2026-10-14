#!/usr/bin/env bash
# Regenerates services/dev-portal-backend/rules.json from the Rego sources:
# per rule in data.gbo.rules its rule_id, regime, package, covers_types,
# covers_fields and, when it names them, services, keyed by package. The dev-portal serves this file at /rules
# with each rule's source — the OpenFTV PDP has no data-API to evaluate it
# live. Run after changing policies/gbo/rules/.
set -euo pipefail
cd "$(dirname "$0")/.."

OPA_IMAGE="${OPA_IMAGE:-openpolicyagent/opa:1.9.0-static}"
OUT=services/dev-portal-backend/rules.json

# services is a second object: a rule that names none has no entry, and
# its absence (every service) differs from an empty set (none).
QUERY='{"rules": {pkg: {
  "rule_id": rid,
  "regime": data.gbo.rules[dir][name].regime,
  "package": pkg,
  "covers_types": data.gbo.rules[dir][name].covers_types,
  "covers_fields": data.gbo.rules[dir][name].covers_fields,
} |
  rid := data.gbo.rules[dir][name].rule_id
  pkg := sprintf("gbo.rules.%s.%s", [dir, name])
}, "services": {pkg: services |
  data.gbo.rules[dir][name].rule_id
  services := data.gbo.rules[dir][name].services
  pkg := sprintf("gbo.rules.%s.%s", [dir, name])
}}'

docker run --rm -v "$PWD/policies:/policies:ro" "$OPA_IMAGE" \
  eval --bundle /policies --format json "$QUERY" \
  | python3 -c '
import json, sys
value = json.load(sys.stdin)["result"][0]["expressions"][0]["value"]
rules = value["rules"]
for pkg, services in value["services"].items():
    rules[pkg]["services"] = services
print(json.dumps(rules, indent=2, sort_keys=True))' \
  > "$OUT"

echo "wrote $OUT"
