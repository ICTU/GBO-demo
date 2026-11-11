-- Each reservation has an owner: only the request that holds it may
-- complete or release it.
ALTER TABLE issued_credentials ADD COLUMN owner text;
