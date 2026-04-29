BEGIN;
ALTER TABLE deployment_tokens DROP COLUMN IF EXISTS require_email;
COMMIT;
