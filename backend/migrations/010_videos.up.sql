-- Onboarding video distribution. Mirrors the ai_packages table — admin
-- uploads a video to a CDN (Bunny / R2 / etc.) and registers the URL
-- here. Agents pick up the active row, download to disk, and play it
-- via ShellExecuteW the first time the machine reports launch_ai.
-- After playback the agent acks /api/v1/agent/video-played which sets
-- machines.video_played_at, so the same employee never sees the same
-- video twice.

CREATE TABLE IF NOT EXISTS videos (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    filename        TEXT        NOT NULL,
    sha256          TEXT        NOT NULL,
    size_bytes      BIGINT      NOT NULL,
    version_label   TEXT        NOT NULL,
    notes           TEXT,
    -- external_url is the CDN location agents pull from. We don't store
    -- bytes server-side for videos at all; the admin uploads to their
    -- CDN out-of-band and registers the URL here.
    external_url    TEXT        NOT NULL,
    uploaded_by     UUID        NOT NULL REFERENCES admin_users(id),
    is_active       BOOLEAN     NOT NULL DEFAULT FALSE,
    revoked_at      TIMESTAMPTZ,
    uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Only one row can be active at a time. Partial unique index because
-- old rows can sit around with is_active=false and we don't want them
-- in the constraint.
CREATE UNIQUE INDEX IF NOT EXISTS idx_videos_one_active
    ON videos (is_active) WHERE is_active = TRUE;

-- Per-machine ack of "I have played the active video". NULL means the
-- machine hasn't seen it yet (or the admin published a fresh video and
-- we cleared the column — see below). Cleared on every video activation
-- so the new video shows up on every machine, not just newly enrolled
-- ones. The clear runs in the same transaction as the activation so
-- the heartbeat fan-out is atomic.
ALTER TABLE machines ADD COLUMN IF NOT EXISTS video_played_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_machines_video_pending
    ON machines (id) WHERE video_played_at IS NULL AND disabled_at IS NULL;
