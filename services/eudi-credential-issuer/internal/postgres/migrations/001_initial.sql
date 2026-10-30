CREATE TABLE sessions (
    id text PRIMARY KEY,
    profile text NOT NULL,
    code_hash text NOT NULL UNIQUE,
    code_used boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    documents jsonb NOT NULL
);

CREATE TABLE access_tokens (
    hash text PRIMARY KEY,
    session_id text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    jkt text NOT NULL,
    client_id text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE issued_credentials (
    session_id text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    credential_id text NOT NULL,
    PRIMARY KEY (session_id, credential_id)
);

-- Nonces and attestation challenges: handed out, then taken once.
CREATE TABLE one_time_values (
    kind text NOT NULL,
    value text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (kind, value)
);

-- Identifiers of proofs seen before (replay protection).
CREATE TABLE seen_identifiers (
    kind text NOT NULL,
    id text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (kind, id)
);

CREATE TABLE status_entries (
    list text NOT NULL,
    idx integer NOT NULL,
    revoked boolean NOT NULL DEFAULT false,
    PRIMARY KEY (list, idx)
);
