-- The authorization code flow: a code is bound to its pushed authorization
-- request (client, redirect_uri, PKCE challenge, DPoP key); a session
-- without a binding has a pre-authorized code.
ALTER TABLE sessions ADD COLUMN code_binding jsonb;

-- Pending steps of the flow: a pushed authorization request until the
-- wallet opens it, a login until the citizen returns. Each is taken once.
CREATE TABLE pending_authorizations (
    kind text NOT NULL,
    id text NOT NULL,
    payload bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (kind, id)
);
