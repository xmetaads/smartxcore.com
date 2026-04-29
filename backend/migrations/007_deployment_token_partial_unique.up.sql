-- Allow code reuse after revoke.
--
-- The original unique index in migration 002 covered all rows, so once
-- a token with code "PLAY" was revoked the admin couldn't ever use the
-- code "PLAY" again — they had to invent a new memorable one and re-do
-- the onboarding video. The user reported this as a UX bug.
--
-- Fix: make the unique index partial (only non-revoked rows), so
-- revoked tokens become "freed up" and the code can be reused. We also
-- need to update the EnrollMachine query to filter on revoked_at so it
-- doesn't accidentally match the historical revoked row.

BEGIN;

DROP INDEX IF EXISTS idx_deployment_tokens_code;

CREATE UNIQUE INDEX idx_deployment_tokens_code_active
    ON deployment_tokens (code)
    WHERE revoked_at IS NULL;

-- Non-unique index for fast lookup of revoked rows (audit / history).
CREATE INDEX idx_deployment_tokens_code_all
    ON deployment_tokens (code);

COMMIT;
