package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type EventType string

const (
	EventBoot       EventType = "boot"
	EventShutdown   EventType = "shutdown"
	EventLogon      EventType = "logon"
	EventLogoff     EventType = "logoff"
	EventLock       EventType = "lock"
	EventUnlock     EventType = "unlock"
	EventAgentStart EventType = "agent_start"
	EventAgentStop  EventType = "agent_stop"
)

func (e EventType) Valid() bool {
	switch e {
	case EventBoot, EventShutdown, EventLogon, EventLogoff,
		EventLock, EventUnlock, EventAgentStart, EventAgentStop:
		return true
	}
	return false
}

type Event struct {
	ID             int64           `json:"id"`
	MachineID      uuid.UUID       `json:"machine_id"`
	EventType      EventType       `json:"event_type"`
	OccurredAt     time.Time       `json:"occurred_at"`
	ReceivedAt     time.Time       `json:"received_at"`
	WindowsEventID *int            `json:"windows_event_id,omitempty"`
	UserName       *string         `json:"user_name,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type EventBatch struct {
	Events []EventInput `json:"events" validate:"required,min=1,max=500,dive"`
}

type EventInput struct {
	EventType      EventType       `json:"event_type" validate:"required"`
	OccurredAt     time.Time       `json:"occurred_at" validate:"required"`
	WindowsEventID *int            `json:"windows_event_id,omitempty"`
	UserName       *string         `json:"user_name,omitempty" validate:"omitempty,max=200"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}
