package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/models"
)

var (
	ErrInvalidOnboardingCode = errors.New("invalid or expired onboarding code")
	ErrMachineNotFound       = errors.New("machine not found")
	ErrInvalidAuthToken      = errors.New("invalid agent auth token")
)

type MachineService struct {
	db          *database.DB
	tokenLength int
}

func NewMachineService(db *database.DB, tokenLength int) *MachineService {
	return &MachineService{db: db, tokenLength: tokenLength}
}

// RegisterResult is returned to callers after a successful registration so
// the handler can both respond to the agent and trigger downstream events
// (welcome email, audit log) without an extra DB roundtrip.
type RegisterResult struct {
	MachineID     uuid.UUID
	AuthToken     string
	EmployeeName  string
	EmployeeEmail string
}

// RegisterMachine consumes an onboarding code and creates a new machine record
// with a unique auth token. Returns the machine ID and the token (token is
// only shown to the agent on registration; after that the agent must store it).
func (s *MachineService) RegisterMachine(
	ctx context.Context,
	onboardingCode string,
	info models.MachineRegisterInfo,
) (*RegisterResult, error) {
	authToken, err := generateToken(s.tokenLength)
	if err != nil {
		return nil, fmt.Errorf("generate auth token: %w", err)
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		tokenID       uuid.UUID
		employeeEmail string
		employeeName  string
		department    *string
		usedAt        *time.Time
		expiresAt     time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id, employee_email, employee_name, department, used_at, expires_at
		FROM onboarding_tokens
		WHERE code = $1
		FOR UPDATE
	`, onboardingCode).Scan(&tokenID, &employeeEmail, &employeeName, &department, &usedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidOnboardingCode
	}
	if err != nil {
		return nil, fmt.Errorf("query onboarding token: %w", err)
	}
	if usedAt != nil {
		return nil, ErrInvalidOnboardingCode
	}
	if time.Now().After(expiresAt) {
		return nil, ErrInvalidOnboardingCode
	}

	var machineID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO machines (
			auth_token, employee_email, employee_name, department,
			hostname, os_version, os_build, cpu_model, ram_total_mb,
			timezone, locale, agent_version, agent_install_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11, $12, NOW()
		)
		RETURNING id
	`,
		authToken, employeeEmail, employeeName, department,
		info.Hostname, info.OSVersion, info.OSBuild, info.CPUModel, info.RAMTotalMB,
		info.Timezone, info.Locale, info.AgentVersion,
	).Scan(&machineID)
	if err != nil {
		return nil, fmt.Errorf("insert machine: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE onboarding_tokens
		SET used_at = NOW(), used_by_machine = $1
		WHERE id = $2
	`, machineID, tokenID)
	if err != nil {
		return nil, fmt.Errorf("mark token used: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return &RegisterResult{
		MachineID:     machineID,
		AuthToken:     authToken,
		EmployeeName:  employeeName,
		EmployeeEmail: employeeEmail,
	}, nil
}

// AuthenticateAgent validates an auth token and returns the machine ID.
// This is called on every agent request (heartbeat, events, command poll).
//
// Soft-deleted (disabled_at IS NOT NULL) machines still authenticate so
// that an accidentally-deleted machine can auto-restore on next heartbeat
// — see RecordHeartbeat which clears disabled_at on success.
func (s *MachineService) AuthenticateAgent(ctx context.Context, authToken string) (uuid.UUID, error) {
	if len(authToken) < 16 {
		return uuid.Nil, ErrInvalidAuthToken
	}

	var machineID uuid.UUID
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id FROM machines WHERE auth_token = $1
	`, authToken).Scan(&machineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidAuthToken
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("authenticate agent: %w", err)
	}
	return machineID, nil
}

// RecordHeartbeat updates the machine's last_seen_at and inserts a heartbeat row.
func (s *MachineService) RecordHeartbeat(
	ctx context.Context,
	machineID uuid.UUID,
	publicIP string,
	req models.HeartbeatRequest,
) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		UPDATE machines
		SET last_seen_at = NOW(),
		    is_online = TRUE,
		    agent_version = $1,
		    public_ip = $2::inet,
		    disabled_at = NULL
		WHERE id = $3
	`, req.AgentVersion, nullableIP(publicIP), machineID)
	if err != nil {
		return fmt.Errorf("update machine: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO heartbeats (machine_id, agent_version, public_ip, cpu_percent, ram_used_mb)
		VALUES ($1, $2, $3::inet, $4, $5)
	`, machineID, req.AgentVersion, nullableIP(publicIP), req.CPUPercent, req.RAMUsedMB)
	if err != nil {
		return fmt.Errorf("insert heartbeat: %w", err)
	}

	return tx.Commit(ctx)
}

// IngestEvents stores a batch of events from an agent.
// Idempotency: agents may resend events on retry, but uniqueness on
// (machine_id, occurred_at, event_type) prevents duplicates at app level.
func (s *MachineService) IngestEvents(
	ctx context.Context,
	machineID uuid.UUID,
	batch []models.EventInput,
) (int, error) {
	if len(batch) == 0 {
		return 0, nil
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inserted := 0
	for _, ev := range batch {
		if !ev.EventType.Valid() {
			continue
		}

		ct, err := tx.Exec(ctx, `
			INSERT INTO events (machine_id, event_type, occurred_at, windows_event_id, user_name, metadata)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING
		`, machineID, ev.EventType, ev.OccurredAt, ev.WindowsEventID, ev.UserName, ev.Metadata)
		if err != nil {
			return 0, fmt.Errorf("insert event: %w", err)
		}
		inserted += int(ct.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

func generateToken(length int) (string, error) {
	if length < 16 {
		length = 16
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:length], nil
}

func nullableIP(s string) any {
	if s == "" {
		return nil
	}
	return s
}
