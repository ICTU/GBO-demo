-- A read-only role on the Authorization Decision Log, for the developer
-- portal. The ADL is an audit store, so a reader gets SELECT and nothing
-- else.
--
-- Idempotent: postgres-ftv-init runs it on every `up`, so a volume that was
-- initialised before this role existed gets it too. The password arrives as
-- the psql variable reader_password.

SELECT format('CREATE ROLE adl_reader LOGIN PASSWORD %L', :'reader_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adl_reader')
\gexec

ALTER ROLE adl_reader PASSWORD :'reader_password';

GRANT CONNECT ON DATABASE ftv_adl TO adl_reader;
GRANT USAGE ON SCHEMA public TO adl_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO adl_reader;
-- The PDP creates its decision table on first start, which can come after
-- this script; the default privilege covers a table created later.
ALTER DEFAULT PRIVILEGES FOR ROLE ftv IN SCHEMA public GRANT SELECT ON TABLES TO adl_reader;
