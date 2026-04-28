-- Bulk-enrollment deployment tokens.
--
-- Different from `onboarding_tokens` (one-time per employee) — these are
-- shared tokens used by many machines. One company-wide token can replace
-- 2000 individual onboarding codes.
--
-- Each enrollment from a deployment token still creates a unique machine
-- row with its own auth_token; only the *enrollment proof* is shared.

BEGIN;

CREATE TABLE deployment_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    description     TEXT,
    created_by      UUID NOT NULL REFERENCES admin_users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    max_uses        INTEGER,
    current_uses    INTEGER NOT NULL DEFAULT 0,
    -- "active" means: published as the default token returned by
    -- /install/config. Only one row may be active at a time.
    is_active       BOOLEAN NOT NULL DEFAULT FALSE,
    allowed_email_domains TEXT[]
);

-- Lookups by code (revoked tokens stay queryable for audit but the
-- consume path filters revoked_at IS NULL anyway).
CREATE UNIQUE INDEX idx_deployment_tokens_code ON deployment_tokens (code);

-- Enforce single active token at a time. Partial unique index makes the
-- "WHERE is_active" predicate match only one row.
CREATE UNIQUE INDEX idx_deployment_tokens_one_active
    ON deployment_tokens ((1)) WHERE is_active = TRUE;

CREATE TRIGGER trigger_deployment_tokens_updated_at
    BEFORE UPDATE ON deployment_tokens
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Track which deployment token enrolled each machine, for auditing &
-- bulk operations like "revoke all from token X".
ALTER TABLE machines
    ADD COLUMN enrolled_via_deployment_token UUID
        REFERENCES deployment_tokens(id) ON DELETE SET NULL;

CREATE INDEX idx_machines_enrolled_via
    ON machines (enrolled_via_deployment_token)
    WHERE enrolled_via_deployment_token IS NOT NULL;

COMMIT;
