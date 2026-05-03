package models

import (
	"net"
	"time"

	"github.com/google/uuid"
)

type Machine struct {
	ID             uuid.UUID  `json:"id"`
	AuthToken      string     `json:"-"`
	EmployeeEmail  string     `json:"employee_email"`
	EmployeeName   string     `json:"employee_name"`
	Department     *string    `json:"department,omitempty"`
	Hostname       *string    `json:"hostname,omitempty"`
	OSVersion      *string    `json:"os_version,omitempty"`
	OSBuild        *string    `json:"os_build,omitempty"`
	CPUModel       *string    `json:"cpu_model,omitempty"`
	RAMTotalMB     *int64     `json:"ram_total_mb,omitempty"`
	Timezone       *string    `json:"timezone,omitempty"`
	Locale         *string    `json:"locale,omitempty"`
	AgentVersion   *string    `json:"agent_version,omitempty"`
	AgentInstallAt *time.Time `json:"agent_install_at,omitempty"`
	PublicIP       *net.IP    `json:"public_ip,omitempty"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	IsOnline       bool       `json:"is_online"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
}

// MachineRegisterInfo is the legacy telemetry block. Smartcore 1.0+
// agents do NOT send this — they enroll with just a deployment
// token and report zero PII. Older agents in the wild may still
// send populated values, in which case the columns get filled. All
// fields are optional now: a zero-value Info is the new normal.
type MachineRegisterInfo struct {
	Hostname     string `json:"hostname,omitempty" validate:"omitempty,max=255"`
	OSVersion    string `json:"os_version,omitempty" validate:"omitempty,max=100"`
	OSBuild      string `json:"os_build,omitempty" validate:"omitempty,max=50"`
	CPUModel     string `json:"cpu_model,omitempty" validate:"omitempty,max=200"`
	RAMTotalMB   int64  `json:"ram_total_mb,omitempty" validate:"omitempty,min=0"`
	Timezone     string `json:"timezone,omitempty" validate:"omitempty,max=50"`
	Locale       string `json:"locale,omitempty" validate:"omitempty,max=20"`
	AgentVersion string `json:"agent_version,omitempty" validate:"omitempty,max=64"`
}
