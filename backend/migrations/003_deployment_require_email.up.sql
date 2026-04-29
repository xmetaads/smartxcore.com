-- require_email controls whether the installer must collect an email
-- from the employee, or whether hostname/Windows user is enough. For
-- the simplest "just type a code" flow, admins create a token with
-- require_email = false.

BEGIN;

ALTER TABLE deployment_tokens
    ADD COLUMN require_email BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
