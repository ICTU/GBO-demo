#!/usr/bin/env bash
# Checks guarantee H3 of the FTV GraphQL profile: every element of the schema
# the PDP validates against (schemas/pdp-mirror) has the same definition in
# the source's running schema. Starts each GraphQL source from the code in
# this checkout and compares it by introspection, so one run covers both
# directions: a change to a copy and a change to a source. Every service in
# services.json must have a source here; a failed or missing check fails.
set -euo pipefail
cd "$(dirname "$0")/.."

docker run --rm -v "$PWD:/w" -w /w golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 sh -euc '
  # service | source directory | port | extra environment
  sources="
bri|graphql-server|4000|
brp|brp-graphql-server|4001|GBO_SOURCE_METADATA_PATH=config/gbo-source-metadata.json
lvg|lvg-graphql-server|4008|LDV_OUTBOX_PATH=/tmp/lvg-outbox.jsonl
"
  catalog=schemas/pdp-mirror
  listed=$(grep -oE "\"[a-z0-9-]+\"[[:space:]]*:[[:space:]]*\{" $catalog/services.json \
    | grep -v "\"services\"" | grep -oE "[a-z0-9-]+" | sort)
  known=$(echo "$sources" | grep "|" | cut -d"|" -f1 | sort)
  if [ "$listed" != "$known" ]; then
    echo "services.json lists [$(echo $listed)], this script checks [$(echo $known)]" >&2
    exit 1
  fi

  (cd services/ftv-graphql-mapper && go build -o /tmp/schemacheck ./cmd/ftv-graphql-schemacheck)
  echo "$sources" | grep "|" | while IFS="|" read -r service dir port extra; do
    (cd "services/$dir" && go build -o "/tmp/$dir" . \
      && env PORT="$port" $extra "/tmp/$dir" >"/tmp/$dir.log" 2>&1 &)
  done

  failed=0
  for line in $(echo "$sources" | grep "|" | cut -d"|" -f1-3); do
    service=${line%%|*}; rest=${line#*|}; dir=${rest%%|*}; port=${rest#*|}
    up=0
    for _ in $(seq 1 120); do
      if wget -q -O /dev/null "http://localhost:$port/health" 2>/dev/null; then up=1; break; fi
      sleep 1
    done
    if [ "$up" != 1 ]; then
      echo "$service: source $dir did not start" >&2
      cat "/tmp/$dir.log" >&2 || true
      failed=1
      continue
    fi
    /tmp/schemacheck -catalog "$catalog" -service "$service" -url "http://localhost:$port/graphql" || failed=1
  done
  exit $failed
'
