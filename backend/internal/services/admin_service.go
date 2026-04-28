package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/models"
)

type AdminService struct {
	db *database.DB
}

func NewAdminService(db *database.DB) *AdminService {
	return &AdminService{db: db}
}

// === Machines ===

type MachineListFilter struct {
	Search       string
	OnlineOnly   bool
	OfflineHours int
	Department   string
	Page         int
	PageSize     int
}

type MachineSummary struct {
	ID            uuid.UUID  `json:"id"`
	EmployeeEmail string     `json:"employee_email"`
	EmployeeName  string     `json:"employee_name"`
	Department    *string    `json:"department,omitempty"`
	Hostname      *string    `json:"hostname,omitempty"`
	OSVersion     *string    `json:"os_version,omitempty"`
	AgentVersion  *string    `json:"agent_version,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	IsOnline      bool       `json:"is_online"`
	CreatedAt     time.Time  `json:"created_at"`
}

type MachineList struct {
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Items    []MachineSummary `json:"items"`
}

func (s *AdminService) ListMachines(ctx context.Context, f MachineListFilter) (*MachineList, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 50
	}

	conds := []string{"disabled_at IS NULL"}
	args := []any{}

	if f.Search != "" {
		args = append(args, "%"+strings.ToLower(f.Search)+"%")
		ph := fmt.Sprintf("$%d", len(args))
		conds = append(conds, fmt.Sprintf("(LOWER(employee_email) LIKE %s OR LOWER(employee_name) LIKE %s OR LOWER(COALESCE(hostname,'')) LIKE %s)", ph, ph, ph))
	}
	if f.OnlineOnly {
		conds = append(conds, "is_online = TRUE")
	}
	if f.OfflineHours > 0 {
		args = append(args, f.OfflineHours)
		conds = append(conds, fmt.Sprintf("(last_seen_at IS NULL OR last_seen_at < NOW() - ($%d || ' hours')::interval)", len(args)))
	}
	if f.Department != "" {
		args = append(args, f.Department)
		conds = append(conds, fmt.Sprintf("department = $%d", len(args)))
	}

	where := strings.Join(conds, " AND ")

	var total int
	if err := s.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM machines WHERE "+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count machines: %w", err)
	}

	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	limitOffset := fmt.Sprintf("LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	query := `
		SELECT id, employee_email, employee_name, department, hostname,
		       os_version, agent_version, last_seen_at, is_online, created_at
		FROM machines
		WHERE ` + where + `
		ORDER BY is_online DESC, last_seen_at DESC NULLS LAST, created_at DESC
		` + limitOffset

	rows, err := s.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list machines: %w", err)
	}
	defer rows.Close()

	items := make([]MachineSummary, 0)
	for rows.Next() {
		var m MachineSummary
		if err := rows.Scan(
			&m.ID, &m.EmployeeEmail, &m.EmployeeName, &m.Department, &m.Hostname,
			&m.OSVersion, &m.AgentVersion, &m.LastSeenAt, &m.IsOnline, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan machine: %w", err)
		}
		items = append(items, m)
	}

	return &MachineList{
		Total:    total,
		Page:     f.Page,
		PageSize: f.PageSize,
		Items:    items,
	}, nil
}

func (s *AdminService) GetMachine(ctx context.Context, id uuid.UUID) (*models.Machine, error) {
	var m models.Machine
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, employee_email, employee_name, department,
		       hostname, os_version, os_build, cpu_model, ram_total_mb,
		       timezone, locale, agent_version, agent_install_at,
		       last_seen_at, is_online, created_at, updated_at, disabled_at
		FROM machines WHERE id = $1
	`, id).Scan(
		&m.ID, &m.EmployeeEmail, &m.EmployeeName, &m.Department,
		&m.Hostname, &m.OSVersion, &m.OSBuild, &m.CPUModel, &m.RAMTotalMB,
		&m.Timezone, &m.Locale, &m.AgentVersion, &m.AgentInstallAt,
		&m.LastSeenAt, &m.IsOnline, &m.CreatedAt, &m.UpdatedAt, &m.DisabledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMachineNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// === Onboarding tokens ===

type OnboardingToken struct {
	ID            uuid.UUID  `json:"id"`
	Code          string     `json:"code"`
	EmployeeEmail string     `json:"employee_email"`
	EmployeeName  string     `json:"employee_name"`
	Department    *string    `json:"department,omitempty"`
	Notes         *string    `json:"notes,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	UsedAt        *time.Time `json:"used_at,omitempty"`
}

type CreateOnboardingTokenInput struct {
	EmployeeEmail string
	EmployeeName  string
	Department    *string
	Notes         *string
	TTLHours      int
	CreatedBy     uuid.UUID
}

func (s *AdminService) CreateOnboardingToken(ctx context.Context, in CreateOnboardingTokenInput) (*OnboardingToken, error) {
	if in.TTLHours <= 0 {
		in.TTLHours = 72
	}
	code, err := generateOnboardingCode()
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(in.TTLHours) * time.Hour)

	var t OnboardingToken
	err = s.db.Pool.QueryRow(ctx, `
		INSERT INTO onboarding_tokens (code, employee_email, employee_name, department, notes, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, code, employee_email, employee_name, department, notes, created_at, expires_at, used_at
	`, code, in.EmployeeEmail, in.EmployeeName, in.Department, in.Notes, in.CreatedBy, expiresAt).Scan(
		&t.ID, &t.Code, &t.EmployeeEmail, &t.EmployeeName, &t.Department, &t.Notes, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create token: %w", err)
	}
	return &t, nil
}

func (s *AdminService) ListOnboardingTokens(ctx context.Context, includeUsed bool, limit int) ([]OnboardingToken, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	where := "1=1"
	if !includeUsed {
		where = "used_at IS NULL AND expires_at > NOW()"
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, code, employee_email, employee_name, department, notes, created_at, expires_at, used_at
		FROM onboarding_tokens
		WHERE `+where+`
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]OnboardingToken, 0)
	for rows.Next() {
		var t OnboardingToken
		if err := rows.Scan(&t.ID, &t.Code, &t.EmployeeEmail, &t.EmployeeName, &t.Department, &t.Notes, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// generateOnboardingCode produces a code like "WT-A3F7-K9B2-X4M1".
// Uses crypto/rand + Crockford base32 alphabet (no I/L/O/U) for readability.
func generateOnboardingCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	const groups = 3
	const groupLen = 4

	parts := []string{"WT"}
	for g := 0; g < groups; g++ {
		buf := make([]byte, groupLen)
		raw := make([]byte, groupLen)
		if _, err := readSecureRandom(raw); err != nil {
			return "", err
		}
		for i := 0; i < groupLen; i++ {
			buf[i] = alphabet[int(raw[i])%len(alphabet)]
		}
		parts = append(parts, string(buf))
	}
	return strings.Join(parts, "-"), nil
}

func readSecureRandom(b []byte) (int, error) {
	return cryptoRandRead(b)
}
