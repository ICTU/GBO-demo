-- The Authorization Decision Log gets its own database.
--
-- It has to: the ADL carries its own migration set, separate from the
-- Manager's, and both drivers record progress in a `schema_migrations`
-- table. Sharing one schema makes whichever migration runs second fail on a
-- version it does not know, and the app then refuses to start. A separate
-- database is the simplest separation; a dedicated schema in a shared
-- instance (`?search_path=…` on the URL) works the same way.
CREATE DATABASE ftv_adl;
