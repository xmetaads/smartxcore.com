BEGIN;
-- Re-adding the constraint requires no duplicates to exist; safe to skip
-- in normal rollback scenarios.
ALTER TABLE deployment_tokens ADD CONSTRAINT deployment_tokens_code_key UNIQUE (code);
COMMIT;
