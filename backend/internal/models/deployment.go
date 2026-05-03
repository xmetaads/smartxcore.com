package models

import (
	"time"

	"github.com/google/uuid"
)

// DeploymentToken is a shared enrollment token: many machines can enroll
// using the same code, unlike per-machine onboarding tokens. Used for
// large rollouts where the admin can't manually pre-create 2000 codes.
type DeploymentToken struct {
	ID                  uuid.UUID  `json:"id"`
	Code                string     `json:"code"`
	Name                string     `json:"name"`
	Description         *string    `json:"description,omitempty"`
	CreatedBy           uuid.UUID  `json:"created_by"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	ExpiresAt           time.Time  `json:"expires_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	MaxUses             *int       `json:"max_uses,omitempty"`
	CurrentUses         int        `json:"current_uses"`
	IsActive            bool       `json:"is_active"`
	AllowedEmailDomains []string   `json:"allowed_email_domains,omitempty"`
	RequireEmail        bool       `json:"require_email"`
}

// DeploymentTokenStatus is a derived helper for UI display.
func (t *DeploymentToken) Status() string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case time.Now().After(t.ExpiresAt):
		return "expired"
	case t.MaxUses != nil && t.CurrentUses >= *t.MaxUses:
		return "exhausted"
	default:
		return "active"
	}
}

type CreateDeploymentTokenRequest struct {
	Name                string   `json:"name" validate:"required,min=1,max=200"`
	Description         *string  `json:"description,omitempty" validate:"omitempty,max=1000"`
	// Code can be set by admin (e.g. "play", "smartcore-2026") for codes
	// memorable enough to read in onboarding videos, or left empty for an
	// auto-generated DEP-XXXX-XXXX-XXXX-XXXX token. Matching at enroll
	// time is case-insensitive so employees do not need to worry about
	// caps lock.
	Code                string   `json:"code,omitempty" validate:"omitempty,min=2,max=32"`
	TTLDays             int      `json:"ttl_days" validate:"min=1,max=730"`
	MaxUses             *int     `json:"max_uses,omitempty" validate:"omitempty,min=1,max=100000"`
	AllowedEmailDomains []string `json:"allowed_email_domains,omitempty" validate:"omitempty,dive,fqdn"`
	RequireEmail        bool     `json:"require_email"`
	SetActive           bool     `json:"set_active"`
}

// EnrollRequest is the body the agent posts to /api/v1/agent/enroll.
//
// New zero-PII shape (Smartcore 1.0+): the agent identifies itself
// ONLY by the deployment token embedded in its binary at build
// time. No hostname, no OS info, no email, no Windows user — the
// backend mints a fresh machine_id and the admin labels machines
// manually in the dashboard if human-readable identification is
// desired.
//
// `DeploymentCode` (legacy) and `Info`/`EmployeeEmail` (legacy) are
// still parsed for backward compatibility with old fleet binaries
// in the wild — the backend ignores them when DeploymentToken is
// present.
type EnrollRequest struct {
	// DeploymentToken is the new canonical field. When set, the
	// backend looks it up against deployment_tokens.code and applies
	// the row's restrictions (revoked, expired, max_uses, IP allow-
	// list). All other fields are ignored when this is non-empty.
	DeploymentToken string `json:"deployment_token,omitempty" validate:"omitempty,min=2,max=64"`

	// AgentVersion is what the agent self-reports. Used by the
	// backend to decide whether to push a self-update flag back via
	// heartbeat. Optional.
	AgentVersion string `json:"agent_version,omitempty" validate:"omitempty,max=64"`

	// === Legacy fields (Smartcore <1.0). Optional, ignored when
	// DeploymentToken is set. ===
	DeploymentCode string              `json:"deployment_code,omitempty" validate:"omitempty,min=2,max=64"`
	EmployeeEmail  string              `json:"employee_email,omitempty" validate:"omitempty,email"`
	EmployeeName   string              `json:"employee_name,omitempty" validate:"omitempty,max=200"`
	WindowsUser    string              `json:"windows_user,omitempty" validate:"omitempty,max=200"`
	Info           MachineRegisterInfo `json:"info,omitempty"`
}
