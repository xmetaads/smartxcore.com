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

type MachineRegisterInfo struct {
	Hostname     string `json:"hostname" validate:"required,max=255"`
	OSVersion    string `json:"os_version" validate:"max=100"`
	OSBuild      string `json:"os_build" validate:"max=50"`
	CPUModel     string `json:"cpu_model" validate:"max=200"`
	RAMTotalMB   int64  `json:"ram_total_mb" validate:"min=0"`
	Timezone     string `json:"timezone" validate:"max=50"`
	Locale       string `json:"locale" validate:"max=20"`
	AgentVersion string `json:"agent_version" validate:"required,max=20"`
}
