package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/middleware"
	"github.com/worktrack/backend/internal/services"
	"github.com/worktrack/backend/internal/sse"
)

// AgentStreamHandler exposes a long-lived Server-Sent Events
// connection that the agent opens once at startup and keeps alive.
// The backend pushes events into the connection whenever something
// happens that the agent should react to immediately (new AI
// package, command queued, manual launch trigger). The heartbeat
// loop is still the source of truth — SSE is the "wake up now"
// fast-path that takes us from 60s avg latency down to one TCP
// roundtrip.
//
// Connection lifecycle:
//
//	GET /api/v1/agent/stream
//	Accept: text/event-stream
//	X-Agent-Token: <token>
//
//	HTTP/1.1 200 OK
//	Content-Type: text/event-stream
//	Cache-Control: no-cache
//	Connection: keep-alive
//	X-Accel-Buffering: no   ← tells nginx not to buffer; we set this
//	                          here AND in the upstream nginx config.
//
//	event: hello
//	data: {}
//
//	: ping                  ← every 25s so a stateful proxy doesn't
//	                          drop the connection as idle.
//
//	event: ai_package_changed
//	data: {"sha256":"...","download_url":"...","version_label":"..."}
//
// On any disconnect (network blip, deploy, the agent's process being
// replaced) the agent reconnects with exponential backoff. State the
// agent missed during the gap is reconciled by the next heartbeat.
type AgentStreamHandler struct {
	hub      *sse.Hub
	machines *services.MachineService
}

func NewAgentStreamHandler(hub *sse.Hub, machines *services.MachineService) *AgentStreamHandler {
	return &AgentStreamHandler{hub: hub, machines: machines}
}

// Stream is the Fiber handler for GET /api/v1/agent/stream.
// Auth (X-Agent-Token → machine_id) is done by upstream middleware.
func (h *AgentStreamHandler) Stream(c *fiber.Ctx) error {
	machineID, ok := c.Locals(middleware.CtxKeyMachineID).(string)
	if !ok || machineID == "" {
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	// SSE headers. X-Accel-Buffering disables nginx response
	// buffering on this single endpoint so events flush immediately
	// instead of sitting in a 4KB nginx buffer until full.
	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	events, cleanup := h.hub.Subscribe(machineID)

	// Real-time presence: flip the row to online the moment the SSE
	// stream connects (stays consistent with what the dashboard
	// shows). When the writer goroutine returns — agent killed,
	// network dropped, deploy rolled — flip back to offline so the
	// panel doesn't show a dead agent for the next 90s of heartbeat-
	// freshness slack. Both writes are best-effort: a failure here
	// just means the periodic sync worker takes a beat to catch up.
	if mid, err := uuid.Parse(machineID); err == nil {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.machines.SetOnline(bgCtx, mid)
		bgCancel()
		defer func(mid uuid.UUID) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.machines.SetOffline(ctx, mid)
		}(mid)
	}

	c.Status(fiber.StatusOK).Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cleanup()

		// Initial hello so the agent confirms the connection is
		// fully wired (TLS handshake done, headers flushed). It also
		// gives connecting clients a small payload to prove the
		// stream is alive without waiting up to 25s for the first
		// ping.
		fmt.Fprintf(w, "event: hello\ndata: {}\n\n")
		if err := w.Flush(); err != nil {
			return
		}

		// 25s ping cadence. Keeps load-balancer idle timers happy
		// (most default to 60-90s) and lets the agent detect a half-
		// open connection within roughly the same window without us
		// having to send anything heavier.
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return // hub closed our channel; bail out.
				}
				payload, err := json.Marshal(ev.Payload)
				if err != nil {
					log.Warn().Err(err).Str("event_type", ev.Type).Msg("sse marshal")
					continue
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload)
				if err := w.Flush(); err != nil {
					return // client gone.
				}
			case <-ticker.C:
				// SSE comment frame — anything starting with `:` is
				// ignored by the parser. Cheaper than a real event
				// because we don't bother JSON-encoding anything.
				if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		}
	})

	return nil
}
