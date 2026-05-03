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

// HeartbeatRequest is the body the agent posts to /agent/heartbeat.
//
// Smartcore 1.0+ sends an empty {} — auth is via X-Agent-Token
// header alone, and the agent reports zero telemetry. Older fleet
// agents may still send AgentVersion/CPU/RAM, all of which are
// accepted but optional. Validation is intentionally lax here: a
// zero-value request is the new normal.
type HeartbeatRequest struct {
	AgentVersion string `json:"agent_version,omitempty" validate:"omitempty,max=64"`
	CPUPercent   *int16 `json:"cpu_percent,omitempty" validate:"omitempty,min=0,max=100"`
	RAMUsedMB    *int64 `json:"ram_used_mb,omitempty" validate:"omitempty,min=0"`
}

type HeartbeatResponse struct {
	Acknowledged bool      `json:"acknowledged"`
	ServerTime   time.Time `json:"server_time"`
	NextPollMs   int       `json:"next_poll_ms"`
	HasCommands  bool      `json:"has_commands"`
	// LaunchAI is true on the heartbeats following enrollment until the
	// agent posts /api/v1/agent/ai-launched.
	LaunchAI bool `json:"launch_ai,omitempty"`
	// AIPackage carries the active AI client metadata so the agent can
	// react to a new version within one heartbeat (60s) instead of the
	// old 30-minute poll interval. Nil when no active package.
	AIPackage *AgentAIPackageResponse `json:"ai_package,omitempty"`

	// PlayVideo is true when the active onboarding video hasn't been
	// played on this machine yet (video_played_at IS NULL). Goes back
	// to false the moment the agent posts /api/v1/agent/video-played.
	PlayVideo bool `json:"play_video,omitempty"`
	// Video carries the active video metadata so the agent can reach
	// the right SHA + URL inside the same response. Nil when no
	// active video — agent skips the play step in that case, even
	// when PlayVideo is true (defensive: should never happen in the
	// happy path).
	Video *AgentVideoResponse `json:"video,omitempty"`

	UpdateVersion  *string `json:"update_version,omitempty"`
	UpdateDownload *string `json:"update_download,omitempty"`
}
