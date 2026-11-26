#!/bin/sh
# Creates the databases of the directory peer and of each org peer.
# Run by the postgres-init one-shot on every `up`; idempotent, so an existing
# volume also gets databases added later.

set -eu

for peer in dir edi bd hv brp cr pdp ir int asip; do
  for role in controller manager txlog; do
    printf "SELECT 'CREATE DATABASE fsc_%s_%s' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'fsc_%s_%s')\\\\gexec\n" \
      "$peer" "$role" "$peer" "$role"
  done
done | psql -v ON_ERROR_STOP=1 --quiet
