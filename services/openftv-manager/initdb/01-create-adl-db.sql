-- The Authorization Decision Log gets its own database.
--
-- It has to: the ADL carries its own migration set (one version), while the
-- Manager's schema is at version 10, and both drivers record progress in a
-- `schema_migrations` table. Sharing a database makes the PDP's ADL
-- migration fail with "no migration found for version 10" and the PDP then
-- refuses to start.
CREATE DATABASE ftv_adl;
