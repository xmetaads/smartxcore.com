-- WorkTrack initial schema
-- PostgreSQL 16+

BEGIN;

CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "citext";

-- =====================================================================
-- Admin users (dashboard login)
-- =====================================================================
CREATE TABLE admin_users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           CITEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    name            TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'viewer')),
    totp_secret     TEXT,
    totp_enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at     TIMESTAMPTZ
);

CREATE INDEX idx_admin_users_email ON admin_users (email) WHERE disabled_at IS NULL;

-- =====================================================================
-- Admin sessions (JWT refresh tokens)
-- =====================================================================
CREATE TABLE admin_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_user_id   UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    refresh_token   TEXT NOT NULL UNIQUE,
    user_agent      TEXT,
    ip_address      INET,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX idx_admin_sessions_token ON admin_sessions (refresh_token) WHERE revoked_at IS NULL;
CREATE INDEX idx_admin_sessions_user ON admin_sessions (admin_user_id);

-- =====================================================================
-- Onboarding tokens (1-time codes for new employees to install agent)
-- =====================================================================
CREATE TABLE onboarding_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    employee_email  CITEXT NOT NULL,
    employee_name   TEXT NOT NULL,
    department      TEXT,
    notes           TEXT,
    created_by      UUID NOT NULL REFERENCES admin_users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    used_by_machine UUID
);

CREATE INDEX idx_onboarding_code ON onboarding_tokens (code) WHERE used_at IS NULL;
CREATE INDEX idx_onboarding_email ON onboarding_tokens (employee_email);

-- =====================================================================
-- Machines (employee endpoints)
-- =====================================================================
CREATE TABLE machines (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    auth_token      TEXT NOT NULL UNIQUE,
    employee_email  CITEXT NOT NULL,
    employee_name   TEXT NOT NULL,
    department      TEXT,

    -- Hardware/OS info reported by agent
    hostname        TEXT,
    os_version      TEXT,
    os_build        TEXT,
    cpu_model       TEXT,
    ram_total_mb    BIGINT,
    timezone        TEXT,
    locale          TEXT,

    -- Agent metadata
    agent_version   TEXT,
    agent_install_at TIMESTAMPTZ,
    public_ip       INET,

    -- Status
    last_seen_at    TIMESTAMPTZ,
    is_online       BOOLEAN NOT NULL DEFAULT FALSE,

    -- Lifecycle
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at     TIMESTAMPTZ
);

CREATE INDEX idx_machines_email ON machines (employee_email);
CREATE INDEX idx_machines_online ON machines (is_online, last_seen_at) WHERE disabled_at IS NULL;
CREATE INDEX idx_machines_last_seen ON machines (last_seen_at) WHERE disabled_at IS NULL;
CREATE UNIQUE INDEX idx_machines_token ON machines (auth_token) WHERE disabled_at IS NULL;

-- =====================================================================
-- Heartbeats (agent ping every 60s) — high write volume
-- Partitioned by month to keep performance, drop old partitions
-- =====================================================================
CREATE TABLE heartbeats (
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    agent_version   TEXT,
    public_ip       INET,
    cpu_percent     SMALLINT,
    ram_used_mb     BIGINT,
    PRIMARY KEY (machine_id, received_at)
) PARTITION BY RANGE (received_at);

CREATE INDEX idx_heartbeats_received ON heartbeats (received_at);

-- Initial partitions (auto-create monthly via cron job)
CREATE TABLE heartbeats_2026_04 PARTITION OF heartbeats
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE heartbeats_2026_05 PARTITION OF heartbeats
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE heartbeats_2026_06 PARTITION OF heartbeats
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

-- =====================================================================
-- Events (boot/shutdown/logon/logoff/lock/unlock)
-- The source of truth for work time calculation
-- =====================================================================
CREATE TYPE event_type AS ENUM (
    'boot',
    'shutdown',
    'logon',
    'logoff',
    'lock',
    'unlock',
    'agent_start',
    'agent_stop'
);

CREATE TABLE events (
    id              BIGSERIAL,
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    event_type      event_type NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    windows_event_id INTEGER,
    user_name       TEXT,
    metadata        JSONB,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX idx_events_machine_time ON events (machine_id, occurred_at DESC);
CREATE INDEX idx_events_type ON events (event_type, occurred_at DESC);

CREATE TABLE events_2026_04 PARTITION OF events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE events_2026_05 PARTITION OF events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE events_2026_06 PARTITION OF events
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

-- =====================================================================
-- Work sessions (computed from events for fast reporting)
-- A session = login until logoff (or shutdown, or current time if active)
-- =====================================================================
CREATE TABLE work_sessions (
    id              BIGSERIAL PRIMARY KEY,
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    employee_email  CITEXT NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL,
    ended_at        TIMESTAMPTZ,
    duration_seconds INTEGER,
    end_reason      TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sessions_machine ON work_sessions (machine_id, started_at DESC);
CREATE INDEX idx_sessions_email ON work_sessions (employee_email, started_at DESC);
CREATE INDEX idx_sessions_active ON work_sessions (is_active) WHERE is_active = TRUE;
CREATE INDEX idx_sessions_started ON work_sessions (started_at);

-- =====================================================================
-- Commands (PowerShell remote execution)
-- =====================================================================
CREATE TYPE command_status AS ENUM (
    'pending',
    'dispatched',
    'running',
    'completed',
    'failed',
    'timeout',
    'cancelled'
);

CREATE TABLE commands (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    created_by      UUID NOT NULL REFERENCES admin_users(id),

    -- Command payload
    script_content  TEXT NOT NULL,
    script_args     TEXT[],
    timeout_seconds INTEGER NOT NULL DEFAULT 300,

    -- Status tracking
    status          command_status NOT NULL DEFAULT 'pending',
    dispatched_at   TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    -- Result
    exit_code       INTEGER,
    stdout          TEXT,
    stderr          TEXT,
    error_message   TEXT,

    -- Metadata
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_commands_machine_status ON commands (machine_id, status, created_at DESC);
CREATE INDEX idx_commands_pending ON commands (machine_id) WHERE status = 'pending';
CREATE INDEX idx_commands_creator ON commands (created_by, created_at DESC);

-- =====================================================================
-- Audit log (every admin action, for compliance)
-- =====================================================================
CREATE TABLE audit_log (
    id              BIGSERIAL PRIMARY KEY,
    admin_user_id   UUID REFERENCES admin_users(id) ON DELETE SET NULL,
    action          TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_id     TEXT,
    metadata        JSONB,
    ip_address      INET,
    user_agent      TEXT,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_user ON audit_log (admin_user_id, occurred_at DESC);
CREATE INDEX idx_audit_action ON audit_log (action, occurred_at DESC);
CREATE INDEX idx_audit_resource ON audit_log (resource_type, resource_id);

-- =====================================================================
-- Alerts (offline > 24h, defender flag, etc.)
-- =====================================================================
CREATE TYPE alert_severity AS ENUM ('info', 'warning', 'critical');
CREATE TYPE alert_status AS ENUM ('open', 'acknowledged', 'resolved');

CREATE TABLE alerts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id      UUID REFERENCES machines(id) ON DELETE CASCADE,
    alert_type      TEXT NOT NULL,
    severity        alert_severity NOT NULL,
    status          alert_status NOT NULL DEFAULT 'open',
    title           TEXT NOT NULL,
    description     TEXT,
    metadata        JSONB,
    triggered_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID REFERENCES admin_users(id),
    resolved_at     TIMESTAMPTZ,
    notified_at     TIMESTAMPTZ
);

CREATE INDEX idx_alerts_status ON alerts (status, severity, triggered_at DESC);
CREATE INDEX idx_alerts_machine ON alerts (machine_id, triggered_at DESC);

-- =====================================================================
-- Settings (system-wide config)
-- =====================================================================
CREATE TABLE settings (
    key             TEXT PRIMARY KEY,
    value           JSONB NOT NULL,
    description     TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by      UUID REFERENCES admin_users(id)
);

INSERT INTO settings (key, value, description) VALUES
    ('heartbeat_interval_seconds', '60', 'Heartbeat frequency from agent'),
    ('command_poll_interval_seconds', '30', 'How often agent checks for new commands'),
    ('offline_alert_threshold_hours', '24', 'Alert if machine offline longer than this'),
    ('agent_latest_version', '"0.1.0"', 'Latest agent version for auto-update'),
    ('alert_email', '"admin@example.com"', 'Email recipient for alerts');

-- =====================================================================
-- Auto-update trigger for updated_at columns
-- =====================================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_admin_users_updated_at
    BEFORE UPDATE ON admin_users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER trigger_machines_updated_at
    BEFORE UPDATE ON machines
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER trigger_work_sessions_updated_at
    BEFORE UPDATE ON work_sessions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER trigger_commands_updated_at
    BEFORE UPDATE ON commands
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
