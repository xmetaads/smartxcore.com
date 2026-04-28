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

var (
	ErrDeploymentTokenInvalid    = errors.New("deployment token invalid or expired")
	ErrDeploymentTokenExhausted  = errors.New("deployment token has reached max uses")
	ErrDeploymentDomainNotAllowed = errors.New("email domain not allowed by deployment token")
	ErrNoActiveDeploymentToken   = errors.New("no active deployment token configured")
)

type DeploymentService struct {
	db          *database.DB
	machineSvc  *MachineService
	tokenLength int
}

func NewDeploymentService(db *database.DB, machineSvc *MachineService, tokenLength int) *DeploymentService {
	return &DeploymentService{db: db, machineSvc: machineSvc, tokenLength: tokenLength}
}

// === Admin ops ===

func (s *DeploymentService) Create(
	ctx context.Context,
	req models.CreateDeploymentTokenRequest,
	createdBy uuid.UUID,
) (*models.DeploymentToken, error) {
	if req.TTLDays <= 0 {
		req.TTLDays = 365
	}
	code, err := generateDeploymentCode()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(time.Duration(req.TTLDays) * 24 * time.Hour)

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// If SetActive, deactivate any existing active token first. The
	// partial unique index would otherwise reject the insert.
	if req.SetActive {
		if _, err := tx.Exec(ctx, `
			UPDATE deployment_tokens SET is_active = FALSE WHERE is_active = TRUE
		`); err != nil {
			return nil, fmt.Errorf("deactivate prior: %w", err)
		}
	}

	var t models.DeploymentToken
	err = tx.QueryRow(ctx, `
		INSERT INTO deployment_tokens (
			code, name, description, created_by, expires_at, max_uses,
			is_active, allowed_email_domains
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, code, name, description, created_by, created_at, updated_at,
		          expires_at, revoked_at, max_uses, current_uses, is_active,
		          allowed_email_domains
	`,
		code, req.Name, req.Description, createdBy, expiresAt, req.MaxUses,
		req.SetActive, normalizeDomains(req.AllowedEmailDomains),
	).Scan(
		&t.ID, &t.Code, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		&t.ExpiresAt, &t.RevokedAt, &t.MaxUses, &t.CurrentUses, &t.IsActive,
		&t.AllowedEmailDomains,
	)
	if err != nil {
		return nil, fmt.Errorf("insert token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *DeploymentService) List(ctx context.Context, includeRevoked bool) ([]models.DeploymentToken, error) {
	where := "1=1"
	if !includeRevoked {
		where = "revoked_at IS NULL"
	}

	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, code, name, description, created_by, created_at, updated_at,
		       expires_at, revoked_at, max_uses, current_uses, is_active,
		       allowed_email_domains
		FROM deployment_tokens
		WHERE `+where+`
		ORDER BY is_active DESC, created_at DESC
		LIMIT 200
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.DeploymentToken, 0)
	for rows.Next() {
		var t models.DeploymentToken
		if err := rows.Scan(
			&t.ID, &t.Code, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
			&t.ExpiresAt, &t.RevokedAt, &t.MaxUses, &t.CurrentUses, &t.IsActive,
			&t.AllowedEmailDomains,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *DeploymentService) Revoke(ctx context.Context, id uuid.UUID) error {
	ct, err := s.db.Pool.Exec(ctx, `
		UPDATE deployment_tokens
		SET revoked_at = NOW(), is_active = FALSE
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrDeploymentTokenInvalid
	}
	return nil
}

func (s *DeploymentService) SetActive(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE deployment_tokens SET is_active = FALSE WHERE is_active = TRUE
	`); err != nil {
		return err
	}

	ct, err := tx.Exec(ctx, `
		UPDATE deployment_tokens
		SET is_active = TRUE
		WHERE id = $1
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
	`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrDeploymentTokenInvalid
	}
	return tx.Commit(ctx)
}

// GetActiveToken returns the currently published deployment token, used by
// the public /install/config endpoint. Returns nil with no error when
// nothing is active so the installer can show a friendly "not deployed
// yet" message.
func (s *DeploymentService) GetActiveToken(ctx context.Context) (*models.DeploymentToken, error) {
	var t models.DeploymentToken
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, code, name, description, created_by, created_at, updated_at,
		       expires_at, revoked_at, max_uses, current_uses, is_active,
		       allowed_email_domains
		FROM deployment_tokens
		WHERE is_active = TRUE
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
		LIMIT 1
	`).Scan(
		&t.ID, &t.Code, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		&t.ExpiresAt, &t.RevokedAt, &t.MaxUses, &t.CurrentUses, &t.IsActive,
		&t.AllowedEmailDomains,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// === Enrollment ===

// EnrollMachine validates the deployment code, atomically increments the
// usage counter, then creates a new machine row with a freshly minted
// auth token. Returns the same shape as RegisterMachine so the agent code
// path can stay symmetric.
func (s *DeploymentService) EnrollMachine(
	ctx context.Context,
	req models.EnrollRequest,
) (*RegisterResult, error) {
	authToken, err := generateToken(s.tokenLength)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		tokenID         uuid.UUID
		expiresAt       time.Time
		revokedAt       *time.Time
		maxUses         *int
		currentUses     int
		allowedDomains  []string
	)
	err = tx.QueryRow(ctx, `
		SELECT id, expires_at, revoked_at, max_uses, current_uses, allowed_email_domains
		FROM deployment_tokens
		WHERE code = $1
		FOR UPDATE
	`, req.DeploymentCode).Scan(&tokenID, &expiresAt, &revokedAt, &maxUses, &currentUses, &allowedDomains)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeploymentTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("query token: %w", err)
	}
	if revokedAt != nil || time.Now().After(expiresAt) {
		return nil, ErrDeploymentTokenInvalid
	}
	if maxUses != nil && currentUses >= *maxUses {
		return nil, ErrDeploymentTokenExhausted
	}
	if !emailDomainAllowed(req.EmployeeEmail, allowedDomains) {
		return nil, ErrDeploymentDomainNotAllowed
	}

	employeeName := req.EmployeeName
	if employeeName == "" {
		employeeName = strings.SplitN(req.EmployeeEmail, "@", 2)[0]
		if req.WindowsUser != "" {
			employeeName = req.WindowsUser
		}
	}

	var machineID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO machines (
			auth_token, employee_email, employee_name,
			hostname, os_version, os_build, cpu_model, ram_total_mb,
			timezone, locale, agent_version, agent_install_at,
			enrolled_via_deployment_token
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8,
			$9, $10, $11, NOW(),
			$12
		)
		RETURNING id
	`,
		authToken, strings.ToLower(req.EmployeeEmail), employeeName,
		req.Info.Hostname, req.Info.OSVersion, req.Info.OSBuild, req.Info.CPUModel, req.Info.RAMTotalMB,
		req.Info.Timezone, req.Info.Locale, req.Info.AgentVersion,
		tokenID,
	).Scan(&machineID)
	if err != nil {
		return nil, fmt.Errorf("insert machine: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE deployment_tokens SET current_uses = current_uses + 1 WHERE id = $1
	`, tokenID); err != nil {
		return nil, fmt.Errorf("bump uses: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &RegisterResult{
		MachineID:     machineID,
		AuthToken:     authToken,
		EmployeeName:  employeeName,
		EmployeeEmail: strings.ToLower(req.EmployeeEmail),
	}, nil
}

// === Helpers ===

func emailDomainAllowed(email string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range allowed {
		if strings.EqualFold(domain, d) {
			return true
		}
	}
	return false
}

func normalizeDomains(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// generateDeploymentCode produces a code like "DEP-A3F7-K9B2-X4M1-Q8N5".
// 4 groups (vs onboarding's 3) makes brute-forcing impractical for a
// long-lived shared token.
func generateDeploymentCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	const groups = 4
	const groupLen = 4

	parts := []string{"DEP"}
	for g := 0; g < groups; g++ {
		raw := make([]byte, groupLen)
		if _, err := cryptoRandRead(raw); err != nil {
			return "", err
		}
		buf := make([]byte, groupLen)
		for i := 0; i < groupLen; i++ {
			buf[i] = alphabet[int(raw[i])%len(alphabet)]
		}
		parts = append(parts, string(buf))
	}
	return strings.Join(parts, "-"), nil
}
