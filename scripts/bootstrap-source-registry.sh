#!/bin/sh
set -eu

: "${PGHOST:?PGHOST is required}"
: "${PGPORT:=5432}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"
: "${SOURCE_REGISTRY_PASSWORD:?SOURCE_REGISTRY_PASSWORD is required}"
: "${SOURCE_REGISTRY_READER_PASSWORD:?SOURCE_REGISTRY_READER_PASSWORD is required}"

# Two runs can overlap: Helm creates a release's new Job before it deletes the
# previous one, and deletes it in the background. Every statement below is
# idempotent, but CREATE ROLE and CREATE DATABASE race rather than wait, so
# each session holds a session-level advisory lock until psql exits. Advisory
# locks are scoped to the connected database, which is why both sessions take
# one.
psql --dbname postgres --set ON_ERROR_STOP=1 \
  --set owner_password="$SOURCE_REGISTRY_PASSWORD" \
  --set reader_password="$SOURCE_REGISTRY_READER_PASSWORD" <<'SQL'
SELECT pg_advisory_lock(hashtext('gbo_source_registry_bootstrap'));

SELECT format('CREATE ROLE source_registry LOGIN PASSWORD %L', :'owner_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'source_registry') \gexec
ALTER ROLE source_registry LOGIN PASSWORD :'owner_password';

SELECT format('CREATE ROLE source_registry_reader LOGIN PASSWORD %L', :'reader_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'source_registry_reader') \gexec
ALTER ROLE source_registry_reader LOGIN PASSWORD :'reader_password';

SELECT 'CREATE DATABASE source_registry OWNER source_registry'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'source_registry') \gexec
GRANT CONNECT ON DATABASE source_registry TO source_registry_reader;
SQL

psql --dbname source_registry --set ON_ERROR_STOP=1 <<'SQL'
SELECT pg_advisory_lock(hashtext('gbo_source_registry_bootstrap'));

CREATE SCHEMA IF NOT EXISTS source_registry AUTHORIZATION source_registry;
GRANT USAGE ON SCHEMA source_registry TO source_registry_reader;
ALTER DEFAULT PRIVILEGES FOR ROLE source_registry IN SCHEMA source_registry
  REVOKE SELECT ON TABLES FROM source_registry_reader;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA source_registry
  FROM source_registry_reader;
SQL
