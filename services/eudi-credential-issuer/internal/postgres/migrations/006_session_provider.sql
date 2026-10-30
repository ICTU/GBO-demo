-- Sessions belong to a provider: a code redeems only at the provider and
-- profile of its offer. Sessions from before this migration get no
-- provider and redeem nowhere; they expire within the offer lifetime.
ALTER TABLE sessions ADD COLUMN provider text NOT NULL DEFAULT '';
ALTER TABLE sessions ALTER COLUMN provider DROP DEFAULT;
