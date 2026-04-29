package models

import (
	"time"

	"github.com/google/uuid"
)

type CommandStatus string

const (
	CommandPending    CommandStatus = "pending"
	CommandDispatched CommandStatus = "dispatched"
	CommandRunning    CommandStatus = "running"
	CommandCompleted  CommandStatus = "completed"
	CommandFailed     CommandStatus = "failed"
	CommandTimeout    CommandStatus = "timeout"
	CommandCancelled  CommandStatus = "cancelled"
)

type CommandKind string

const (
	// CommandKindExec is the only supported command kind.
	//
	// The agent spawns the binary at script_content with script_args as
	// positional arguments. No shell, no script interpretation. The
	// agent binary contains zero references to powershell.exe, cmd.exe,
	// or any script host so it passes Microsoft Defender static analysis
	// cleanly when submitted for whitelisting.
	CommandKindExec CommandKind = "exec"
)

func (k CommandKind) Valid() bool {
	return k == CommandKindExec
}

type Command struct {
	ID             uuid.UUID     `json:"id"`
	MachineID      uuid.UUID     `json:"machine_id"`
	CreatedBy      uuid.UUID     `json:"created_by"`
	Kind           CommandKind   `json:"kind"`
	ScriptContent  string        `json:"script_content"`
	ScriptArgs     []string      `json:"script_args,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds"`
	Status         CommandStatus `json:"status"`
	DispatchedAt   *time.Time    `json:"dispatched_at,omitempty"`
	StartedAt      *time.Time    `json:"started_at,omitempty"`
	CompletedAt    *time.Time    `json:"completed_at,omitempty"`
	ExitCode       *int          `json:"exit_code,omitempty"`
	Stdout         *string       `json:"stdout,omitempty"`
	Stderr         *string       `json:"stderr,omitempty"`
	ErrorMessage   *string       `json:"error_message,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type CommandCreateRequest struct {
	MachineIDs     []uuid.UUID `json:"machine_ids" validate:"required,min=1,max=2000"`
	Kind           CommandKind `json:"kind" validate:"required,oneof=exec"`
	ScriptContent  string      `json:"script_content" validate:"required,min=1,max=4000"`
	ScriptArgs     []string    `json:"script_args,omitempty" validate:"max=64,dive,max=4000"`
	TimeoutSeconds int         `json:"timeout_seconds" validate:"min=10,max=3600"`
}

type CommandResultRequest struct {
	ExitCode  int       `json:"exit_code"`
	Stdout    string    `json:"stdout" validate:"max=1000000"`
	Stderr    string    `json:"stderr" validate:"max=1000000"`
	StartedAt time.Time `json:"started_at" validate:"required"`
	EndedAt   time.Time `json:"ended_at" validate:"required"`
}

type CommandPollResponse struct {
	Commands []CommandDispatch `json:"commands"`
}

// CommandDispatch is the payload the agent receives when polling.
// Includes Kind so the agent picks the right execution path.
type CommandDispatch struct {
	ID             uuid.UUID   `json:"id"`
	Kind           CommandKind `json:"kind"`
	ScriptContent  string      `json:"script_content"`
	ScriptArgs     []string    `json:"script_args,omitempty"`
	TimeoutSeconds int         `json:"timeout_seconds"`
}
