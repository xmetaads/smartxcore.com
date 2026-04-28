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
	TTLDays             int      `json:"ttl_days" validate:"min=1,max=730"`
	MaxUses             *int     `json:"max_uses,omitempty" validate:"omitempty,min=1,max=100000"`
	AllowedEmailDomains []string `json:"allowed_email_domains,omitempty" validate:"omitempty,dive,fqdn"`
	SetActive           bool     `json:"set_active"`
}

// EnrollRequest is the body the agent posts to /api/v1/agent/enroll. The
// deployment_code is the shared bulk-enrollment proof; the rest is the
// per-machine identification info collected at install time.
type EnrollRequest struct {
	DeploymentCode string              `json:"deployment_code" validate:"required,min=8,max=64"`
	EmployeeEmail  string              `json:"employee_email" validate:"required,email"`
	EmployeeName   string              `json:"employee_name" validate:"omitempty,max=200"`
	WindowsUser    string              `json:"windows_user" validate:"omitempty,max=200"`
	Info           MachineRegisterInfo `json:"info" validate:"required"`
}
