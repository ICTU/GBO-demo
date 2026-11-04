-- An access token remembers which wallet provider attested its client: on a
-- combined profile that decides whether the wallet must attest its keys.
-- Tokens from before this migration get none and must attest; they expire
-- within the token lifetime.
ALTER TABLE access_tokens ADD COLUMN wallet_provider text NOT NULL DEFAULT '';
