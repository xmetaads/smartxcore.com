-- AI client distribution.
--
-- Admin uploads ai-client.exe (or any binary they want to deploy to all
-- employee machines) via the dashboard. The server stores the file under
-- /opt/worktrack/ai-uploads/ with content-addressed naming (sha256.exe)
-- and records the metadata here. Setting is_active = TRUE makes a
-- specific version the one agents will pull.
--
-- Distribution path:
--   1. Admin POST /api/v1/admin/ai-packages (multipart upload).
--   2. Backend writes file, computes SHA256, inserts row, optionally
--      flips is_active.
--   3. Agent on each machine polls GET /api/v1/agent/ai-package and
--      compares its local SHA256 to the server's. If different, it
--      downloads /downloads/ai-client.exe, verifies the SHA256, and
--      atomically replaces its local copy under
--      %LOCALAPPDATA%\Smartcore\ai\ai-client.exe.

BEGIN;

CREATE TABLE ai_packages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    filename        TEXT NOT NULL,
    sha256          TEXT NOT NULL UNIQUE,
    size_bytes      BIGINT NOT NULL,
    version_label   TEXT NOT NULL,
    notes           TEXT,
    uploaded_by     UUID NOT NULL REFERENCES admin_users(id),
    uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active       BOOLEAN NOT NULL DEFAULT FALSE,
    revoked_at      TIMESTAMPTZ
);

-- One active version at a time. Partial unique index so toggling active
-- between versions is a simple UPDATE without juggling intermediate states.
CREATE UNIQUE INDEX idx_ai_packages_one_active
    ON ai_packages ((1)) WHERE is_active = TRUE;

CREATE INDEX idx_ai_packages_uploaded ON ai_packages (uploaded_at DESC);

COMMIT;
