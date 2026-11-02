-- The metadata leg and the data leg of a source authenticate independently.
-- One boolean could not express an FSC-authenticated data call whose
-- describing document arrived over an unauthenticated metadata transport.
--
-- Existing rows were written when both legs shared one transport, so copying
-- the old value is an exact backfill rather than an assumption.
ALTER TABLE source_candidates
    ADD COLUMN data_transport_authenticated boolean NOT NULL DEFAULT false;
UPDATE source_candidates SET data_transport_authenticated = transport_authenticated;
ALTER TABLE source_candidates ALTER COLUMN data_transport_authenticated DROP DEFAULT;

ALTER TABLE source_statuses
    ADD COLUMN data_transport_authenticated boolean NOT NULL DEFAULT false;
UPDATE source_statuses SET data_transport_authenticated = transport_authenticated;
ALTER TABLE source_statuses ALTER COLUMN data_transport_authenticated DROP DEFAULT;

-- Releases are immutable, so the backfilled value stays what it was when the
-- release was promoted. The column sits outside the release digest document;
-- adding it therefore does not invalidate a stored release ID.
ALTER TABLE source_release_sources
    ADD COLUMN data_transport_authenticated boolean NOT NULL DEFAULT false;
UPDATE source_release_sources SET data_transport_authenticated = transport_authenticated;
ALTER TABLE source_release_sources ALTER COLUMN data_transport_authenticated DROP DEFAULT;
