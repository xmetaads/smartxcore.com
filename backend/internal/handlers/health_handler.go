package handlers

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/worktrack/backend/internal/database"
)

type HealthHandler struct {
	db      *database.DB
	version string
}

func NewHealthHandler(db *database.DB, version string) *HealthHandler {
	return &HealthHandler{db: db, version: version}
}

// Live indicates the process is running. Used by load balancer / k8s liveness.
func (h *HealthHandler) Live(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":  "ok",
		"version": h.version,
		"time":    time.Now().UTC(),
	})
}

// Ready indicates the service can serve traffic (db reachable).
// Used for k8s readiness probes and pre-cutover health checks.
func (h *HealthHandler) Ready(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()

	if err := h.db.Pool.Ping(ctx); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "unavailable",
			"error":  "database unreachable",
		})
	}

	return c.JSON(fiber.Map{
		"status":  "ready",
		"version": h.version,
	})
}
