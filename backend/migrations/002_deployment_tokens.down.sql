BEGIN;

ALTER TABLE machines DROP COLUMN IF EXISTS enrolled_via_deployment_token;

DROP TABLE IF EXISTS deployment_tokens CASCADE;

COMMIT;
