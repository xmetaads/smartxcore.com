package services

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/models"
)

var (
	ErrDeploymentTokenInvalid     = errors.New("deployment token invalid or expired")
	ErrDeploymentTokenExhausted   = errors.New("deployment token has reached max uses")
	ErrDeploymentDomainNotAllowed = errors.New("email domain not allowed by deployment token")
	ErrDeploymentEmailRequired    = errors.New("this deployment requires an employee email")
	ErrDeploymentCodeFormat       = errors.New("code may only contain letters, digits, hyphens and underscores")
	ErrDeploymentCodeTaken        = errors.New("a deployment token with that code already exists")
	ErrNoActiveDeploymentToken    = errors.New("no active deployment token configured")
)

// codeFormat allows letters, digits, hyphens, underscores; 2-32 chars.
// Codes are normalised to upper case at creation so case-insensitive
// match at enroll time is a simple equality check.
var codeFormat = regexp.MustCompile(`^[A-Z0-9_-]{2,32}$`)

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

	// Choose / validate code. If admin gave one, accept it (uppercased);
	// otherwise generate a random DEP-XXXX-XXXX-XXXX-XXXX value.
	var code string
	if req.Code != "" {
		code = strings.ToUpper(strings.TrimSpace(req.Code))
		if !codeFormat.MatchString(code) {
			return nil, ErrDeploymentCodeFormat
		}
	} else {
		generated, err := generateDeploymentCode()
		if err != nil {
			return nil, err
		}
		code = generated
	}

	expiresAt := time.Now().Add(time.Duration(req.TTLDays) * 24 * time.Hour)

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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
			is_active, allowed_email_domains, require_email
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, code, name, description, created_by, created_at, updated_at,
		          expires_at, revoked_at, max_uses, current_uses, is_active,
		          allowed_email_domains, require_email
	`,
		code, req.Name, req.Description, createdBy, expiresAt, req.MaxUses,
		req.SetActive, normalizeDomains(req.AllowedEmailDomains), req.RequireEmail,
	).Scan(
		&t.ID, &t.Code, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		&t.ExpiresAt, &t.RevokedAt, &t.MaxUses, &t.CurrentUses, &t.IsActive,
		&t.AllowedEmailDomains, &t.RequireEmail,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDeploymentCodeTaken
		}
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
		       allowed_email_domains, require_email
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
			&t.AllowedEmailDomains, &t.RequireEmail,
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
		WHERE id = $1 AND revoked_at IS NULL AND expires_at > NOW()
	`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrDeploymentTokenInvalid
	}
	return tx.Commit(ctx)
}

func (s *DeploymentService) GetActiveToken(ctx context.Context) (*models.DeploymentToken, error) {
	var t models.DeploymentToken
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, code, name, description, created_by, created_at, updated_at,
		       expires_at, revoked_at, max_uses, current_uses, is_active,
		       allowed_email_domains, require_email
		FROM deployment_tokens
		WHERE is_active = TRUE AND revoked_at IS NULL AND expires_at > NOW()
		LIMIT 1
	`).Scan(
		&t.ID, &t.Code, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		&t.ExpiresAt, &t.RevokedAt, &t.MaxUses, &t.CurrentUses, &t.IsActive,
		&t.AllowedEmailDomains, &t.RequireEmail,
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

// EnrollMachine validates the deployment code (case-insensitive),
// atomically increments the usage counter, then creates a new machine
// row with a freshly minted auth token.
//
// Identity rules:
//   - If the token has require_email=true, EmployeeEmail is mandatory.
//   - Otherwise we synthesise <windows_user>@<hostname>.local when no
//     email is provided so every machine still has a stable identifier.
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

	// hasToken == false means the caller chose the tokenless path:
	// empty deployment_code, no token row to look up, no token
	// restrictions to enforce. Just create the machine.
	hasToken := strings.TrimSpace(req.DeploymentCode) != ""

	var (
		tokenID        uuid.UUID  // zero UUID when hasToken == false
		expiresAt      time.Time
		revokedAt      *time.Time
		maxUses        *int
		currentUses    int
		allowedDomains []string
		requireEmail   bool
	)
	if hasToken {
		// Filter revoked_at IS NULL so we don't accidentally pick up
		// an older revoked token with the same code — admins can
		// reuse codes after revoking, so multiple historical rows
		// can share the value.
		err = tx.QueryRow(ctx, `
			SELECT id, expires_at, revoked_at, max_uses, current_uses,
			       allowed_email_domains, require_email
			FROM deployment_tokens
			WHERE code = $1 AND revoked_at IS NULL
			FOR UPDATE
		`, strings.ToUpper(strings.TrimSpace(req.DeploymentCode))).Scan(
			&tokenID, &expiresAt, &revokedAt, &maxUses, &currentUses, &allowedDomains, &requireEmail,
		)
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
	}

	email := strings.ToLower(strings.TrimSpace(req.EmployeeEmail))
	if requireEmail && email == "" {
		return nil, ErrDeploymentEmailRequired
	}
	if email != "" && len(allowedDomains) > 0 && !emailDomainAllowed(email, allowedDomains) {
		return nil, ErrDeploymentDomainNotAllowed
	}
	if email == "" {
		email = synthesiseIdentity(req.WindowsUser, req.Info.Hostname)
	}

	employeeName := req.EmployeeName
	if employeeName == "" {
		employeeName = req.WindowsUser
	}
	if employeeName == "" {
		employeeName = strings.SplitN(email, "@", 2)[0]
	}

	// tokenIDArg is *uuid.UUID so nil → SQL NULL when the caller
	// took the tokenless path. The schema column is nullable
	// already (machines.enrolled_via_deployment_token uuid).
	var tokenIDArg *uuid.UUID
	if hasToken {
		tokenIDArg = &tokenID
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
		authToken, email, employeeName,
		req.Info.Hostname, req.Info.OSVersion, req.Info.OSBuild, req.Info.CPUModel, req.Info.RAMTotalMB,
		req.Info.Timezone, req.Info.Locale, req.Info.AgentVersion,
		tokenIDArg,
	).Scan(&machineID)
	if err != nil {
		return nil, fmt.Errorf("insert machine: %w", err)
	}

	if hasToken {
		if _, err := tx.Exec(ctx, `
			UPDATE deployment_tokens SET current_uses = current_uses + 1 WHERE id = $1
		`, tokenID); err != nil {
			return nil, fmt.Errorf("bump uses: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &RegisterResult{
		MachineID:     machineID,
		AuthToken:     authToken,
		EmployeeName:  employeeName,
		EmployeeEmail: email,
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

// synthesiseIdentity builds an "email-shape" identifier when no real
// email is provided. Three modes:
//
//  1. Both winUser and hostname populated (legacy agent telemetry):
//     "tom@desktop-ab12.local"
//  2. One populated, one empty: substitute "user" or "machine".
//  3. Both empty (Smartcore 1.0+ zero-PII enrol): generate a random
//     id like "machine-a3f7k9b2@smartcore.local". Per-machine
//     uniqueness via crypto/rand keeps the employee_email column
//     usable as a stable label even when no telemetry is sent.
func synthesiseIdentity(winUser, hostname string) string {
	user := strings.ToLower(strings.TrimSpace(winUser))
	host := strings.ToLower(strings.TrimSpace(hostname))
	if user == "" && host == "" {
		// Zero-PII path: generate a unique random label.
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err == nil {
			return fmt.Sprintf("machine-%x@smartcore.local", buf[:])
		}
		// crypto/rand failure is exceptional; fall through to the
		// least-bad deterministic fallback.
	}
	if user == "" {
		user = "user"
	}
	if host == "" {
		host = "machine"
	}
	return fmt.Sprintf("%s@%s.local", user, host)
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505). We use a string match instead of
// importing pgconn here because the service file already stays free of
// driver-specific imports.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLSTATE 23505")
}

// generateDeploymentCode produces "DEP-A3F7-K9B2-X4M1-Q8N5". Used when
// admin doesn't pass a custom code.
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
