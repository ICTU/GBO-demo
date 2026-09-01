#!/bin/sh
set -eu

: "${PGHOST:?PGHOST is required}"
: "${PGPORT:=5432}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"
: "${SOURCE_REGISTRY_PASSWORD:?SOURCE_REGISTRY_PASSWORD is required}"
: "${SOURCE_REGISTRY_READER_PASSWORD:?SOURCE_REGISTRY_READER_PASSWORD is required}"

psql --dbname postgres --set ON_ERROR_STOP=1 \
  --set owner_password="$SOURCE_REGISTRY_PASSWORD" \
  --set reader_password="$SOURCE_REGISTRY_READER_PASSWORD" <<'SQL'
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
CREATE SCHEMA IF NOT EXISTS source_registry AUTHORIZATION source_registry;
GRANT USAGE ON SCHEMA source_registry TO source_registry_reader;
ALTER DEFAULT PRIVILEGES FOR ROLE source_registry IN SCHEMA source_registry
  REVOKE SELECT ON TABLES FROM source_registry_reader;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA source_registry
  FROM source_registry_reader;
SQL
