BEGIN;
DROP INDEX IF EXISTS idx_deployment_tokens_code_all;
DROP INDEX IF EXISTS idx_deployment_tokens_code_active;
CREATE UNIQUE INDEX idx_deployment_tokens_code ON deployment_tokens (code);
COMMIT;
