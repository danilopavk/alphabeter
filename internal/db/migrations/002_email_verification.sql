-- NULL = not verified yet. Accounts that existed before verification was added
-- stay NULL on purpose: they verify like new accounts.
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
