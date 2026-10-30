-- The offer caller's reference, so its credentials can be revoked together.
ALTER TABLE sessions ADD COLUMN reference text NOT NULL DEFAULT '';
ALTER TABLE status_entries ADD COLUMN reference text NOT NULL DEFAULT '';
CREATE INDEX status_entries_reference ON status_entries (reference) WHERE reference <> '';
