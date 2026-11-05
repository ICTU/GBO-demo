-- A session whose source has not answered yet (deferred issuance): the
-- source offer it waits for. The BSN for the retry is not here: it is kept
-- sealed in pending_authorizations and expires with the deferral.
ALTER TABLE sessions ADD COLUMN pending_source jsonb;
