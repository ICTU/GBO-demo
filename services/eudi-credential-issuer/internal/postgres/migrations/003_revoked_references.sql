-- Revoked offer references: nothing more is issued for them.
CREATE TABLE revoked_references (
    reference text PRIMARY KEY,
    revoked_at timestamptz NOT NULL DEFAULT now()
);
