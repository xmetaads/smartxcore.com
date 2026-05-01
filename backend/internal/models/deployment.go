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
// DeploymentCode is OPTIONAL. When non-empty, the backend looks up
// the matching deployment_tokens row and applies its restrictions
// (revoked, expired, max_uses, require_email,
// allowed_email_domains). When empty, the enroll succeeds without
// any token check — the simplest install flow, where the URL of
// setup.exe itself is the deployment secret. Admins who want IP/
// email restrictions create a token in /deployment and bake it
// into setup.exe at build time.
//
// Email is optional in either path — when not provided we fall
// back to "<windows_user>@<hostname>" so every machine still has a
// unique-ish identifier.
type EnrollRequest struct {
	DeploymentCode string              `json:"deployment_code,omitempty" validate:"omitempty,min=2,max=64"`
	EmployeeEmail  string              `json:"employee_email,omitempty" validate:"omitempty,email"`
	EmployeeName   string              `json:"employee_name,omitempty" validate:"omitempty,max=200"`
	WindowsUser    string              `json:"windows_user,omitempty" validate:"omitempty,max=200"`
	Info           MachineRegisterInfo `json:"info" validate:"required"`
}
