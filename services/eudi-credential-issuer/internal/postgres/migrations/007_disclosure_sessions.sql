-- Disclosure sessions: one OpenID4VP request for a PID, from the moment a
-- wallet fetches it until its response arrives. A response takes the row,
-- so a session answers once; the response key is the session's ephemeral
-- private key and lives no longer than the session.
CREATE TABLE disclosure_sessions (
    id text PRIMARY KEY,
    provider text NOT NULL,
    profile text NOT NULL,
    offer text NOT NULL,
    nonce text NOT NULL,
    state text NOT NULL,
    response_key_id text NOT NULL,
    response_key bytea NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX disclosure_sessions_expires_at ON disclosure_sessions (expires_at);
