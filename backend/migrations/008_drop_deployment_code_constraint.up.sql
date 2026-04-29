-- Drop the table-level UNIQUE constraint on deployment_tokens.code that
-- was created implicitly by `code TEXT NOT NULL UNIQUE` in migration 002.
-- It blocks code reuse after revoke even though migration 007 added a
-- partial unique index that DOES allow reuse — Postgres applies both
-- constraints, and the unconditional one always wins.

BEGIN;

ALTER TABLE deployment_tokens DROP CONSTRAINT IF EXISTS deployment_tokens_code_key;

COMMIT;
