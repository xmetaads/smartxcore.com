// Package sse implements a tiny in-process pub/sub for pushing
// events from the backend to connected agents in real time.
//
// Each agent that calls GET /api/v1/agent/stream gets a long-lived
// HTTP response (Server-Sent Events). The handler subscribes to the
// hub on connect and unsubscribes on disconnect; meanwhile any code
// path that mutates state the agent cares about (a new AI package
// being activated, a command being queued for a specific machine,
// the admin clicking "launch AI now") publishes an event into the
// hub and every relevant agent receives it within a TCP roundtrip.
//
// Design notes:
//   - One channel per connected machine. A buffer of 16 absorbs a
//     burst of broadcasts; if it overflows the oldest event drops
//     rather than blocking the publisher (we never want a slow
//     agent to stall the activation path).
//   - Subscribe returns a cleanup func the handler MUST defer. We
//     don't ref-count by machine ID — if the same machine reconnects
//     mid-flight it gets a new channel and the old one is garbage-
//     collected when its goroutine exits.
//   - Broadcast is fire-and-forget. Clients that miss a push due to
//     full buffer or disconnection still get the same state on the
//     next heartbeat (60s fallback).
package sse

import (
	"sync"
)

// Event is what a publisher hands to the hub. Type is a short
// identifier the agent dispatches on ("ai_package_changed",
// "launch_ai", "command_pending"). Payload is whatever JSON-encoded
// data the agent needs — the SSE handler marshals it once per send.
type Event struct {
	Type    string
	Payload any
}

// Hub fans out events to subscribed machine channels.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]chan Event // machineID → buffered event channel
}

func NewHub() *Hub {
	return &Hub{clients: make(map[string]chan Event)}
}

// Subscribe registers a fresh channel for machineID and returns it
// alongside a cleanup func. If a previous channel exists for this
// machine (rare, only on rapid reconnect) it is closed first so its
// goroutine exits cleanly.
func (h *Hub) Subscribe(machineID string) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	if old, ok := h.clients[machineID]; ok {
		close(old)
	}
	h.clients[machineID] = ch
	h.mu.Unlock()

	cleanup := func() {
		h.mu.Lock()
		// Only delete if this is still the active channel. A racing
		// reconnect may have replaced it; in that case we leave the
		// new entry alone.
		if cur, ok := h.clients[machineID]; ok && cur == ch {
			delete(h.clients, machineID)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, cleanup
}

// SendToMachine pushes one event to a specific machine. No-op if the
// machine has no active SSE connection (the heartbeat fallback will
// still deliver state on its next tick).
//
// Non-blocking: a slow consumer's events are dropped rather than
// stalling the publisher.
func (h *Hub) SendToMachine(machineID string, ev Event) {
	h.mu.RLock()
	ch, ok := h.clients[machineID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case ch <- ev:
	default:
		// buffer full — drop. Heartbeat will reconcile.
	}
}

// BroadcastAll pushes one event to every connected machine. Used for
// fleet-wide notifications like "new AI package is active".
//
// Same non-blocking guarantee per channel: a slow consumer doesn't
// stall fast ones. We hold the read lock for the duration of the fan-
// out which is fine because select+default is O(1).
func (h *Hub) BroadcastAll(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.clients {
		select {
		case ch <- ev:
		default:
		}
	}
}

// ConnectedCount returns the number of currently subscribed machines.
// Used by /healthz so we can monitor SSE connection counts in dashboards.
func (h *Hub) ConnectedCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
