#!/bin/sh
set -eu

: "${PGHOST:?PGHOST is required}"
: "${PGPORT:=5432}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"
: "${CREDENTIAL_ISSUER_DB_PASSWORD:?CREDENTIAL_ISSUER_DB_PASSWORD is required}"

# The own credential issuer gets its own role and database; it creates its
# schema and tables itself (eudi-credential-issuer migrate). Every statement
# is idempotent; the advisory lock makes overlapping runs wait instead of
# racing on CREATE ROLE and CREATE DATABASE.
psql --dbname postgres --set ON_ERROR_STOP=1 \
  --set issuer_password="$CREDENTIAL_ISSUER_DB_PASSWORD" <<'SQL'
SELECT pg_advisory_lock(hashtext('gbo_credential_issuer_bootstrap'));

SELECT format('CREATE ROLE credential_issuer LOGIN PASSWORD %L', :'issuer_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'credential_issuer') \gexec
ALTER ROLE credential_issuer LOGIN PASSWORD :'issuer_password';

SELECT 'CREATE DATABASE credential_issuer OWNER credential_issuer'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'credential_issuer') \gexec
SQL
