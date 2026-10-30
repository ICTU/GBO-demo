-- A credential is reserved before anything is allocated for it, and marked
-- issued when it is signed; a failed attempt removes its reservation.
ALTER TABLE issued_credentials ADD COLUMN state text NOT NULL DEFAULT 'issued';
ALTER TABLE issued_credentials ADD COLUMN reserved_until timestamptz;
