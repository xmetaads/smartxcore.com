package models

import (
	"net"
	"time"

	"github.com/google/uuid"
)

type Heartbeat struct {
	MachineID    uuid.UUID `json:"machine_id"`
	ReceivedAt   time.Time `json:"received_at"`
	AgentVersion *string   `json:"agent_version,omitempty"`
	PublicIP     *net.IP   `json:"public_ip,omitempty"`
	CPUPercent   *int16    `json:"cpu_percent,omitempty"`
	RAMUsedMB    *int64    `json:"ram_used_mb,omitempty"`
}

type HeartbeatRequest struct {
	AgentVersion string `json:"agent_version" validate:"required,max=20"`
	CPUPercent   *int16 `json:"cpu_percent,omitempty" validate:"omitempty,min=0,max=100"`
	RAMUsedMB    *int64 `json:"ram_used_mb,omitempty" validate:"omitempty,min=0"`
}

type HeartbeatResponse struct {
	Acknowledged bool      `json:"acknowledged"`
	ServerTime   time.Time `json:"server_time"`
	NextPollMs   int       `json:"next_poll_ms"`
	HasCommands  bool      `json:"has_commands"`
	// LaunchAI is true on the heartbeats following enrollment until the
	// agent posts /api/v1/agent/ai-launched. After that the server
	// flips ai_launched_at and the flag stays false — the AI client is
	// not restarted by the agent on subsequent heartbeats.
	LaunchAI       bool    `json:"launch_ai,omitempty"`
	UpdateVersion  *string `json:"update_version,omitempty"`
	UpdateDownload *string `json:"update_download,omitempty"`
}
