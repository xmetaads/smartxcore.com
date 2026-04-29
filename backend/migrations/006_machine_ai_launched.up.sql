-- Track whether the AI client has been successfully launched on each
-- machine. The flow is one-shot:
--
--   1. Employee installs Smartcore. Agent connects.
--   2. Server sees ai_launched_at IS NULL on this machine.
--   3. Heartbeat response carries launch_ai=true.
--   4. Agent spawns ai-client.exe detached.
--   5. On success, agent posts /api/v1/agent/ai-launched, server sets
--      ai_launched_at = NOW().
--   6. From this point on, heartbeats return launch_ai=false; the AI
--      client is never restarted by the agent. If admin needs to
--      relaunch (e.g. after a manual update) they reset the column
--      via dashboard / SQL.
--
-- This is what the user requested: launch exactly once. Don't loop
-- restarts. Don't disturb the AI training session.

BEGIN;

ALTER TABLE machines ADD COLUMN ai_launched_at TIMESTAMPTZ;

CREATE INDEX idx_machines_ai_pending
    ON machines (id)
    WHERE ai_launched_at IS NULL AND disabled_at IS NULL;

COMMIT;
