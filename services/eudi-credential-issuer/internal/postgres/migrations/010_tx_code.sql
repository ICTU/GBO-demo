-- A pre-authorized offer may carry a transaction code (OpenID4VCI 1.0
-- section 4.1.1): only its hash is kept, with the wrong attempts; the
-- last allowed failure uses the code up.
ALTER TABLE sessions ADD COLUMN tx_code_hash text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN tx_code_attempts integer NOT NULL DEFAULT 0;
