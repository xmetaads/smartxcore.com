package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/auth"
	"github.com/worktrack/backend/internal/middleware"
	"github.com/worktrack/backend/internal/services"
)

// SettingsHandler exposes the global feature-flag toggle to the admin
// dashboard. Today there is one flag — ai_dispatch_enabled — used to
// disable AI fan-out while submitting binaries to the Microsoft
// Defender Submission Portal. Adding more flags later means another
// JSON field on the snapshot + a sibling toggle endpoint.
type SettingsHandler struct {
	settings *services.SystemSettingsService
}

func NewSettingsHandler(settings *services.SystemSettingsService) *SettingsHandler {
	return &SettingsHandler{settings: settings}
}

// GetSettings returns the current snapshot for the dashboard. Auth-
// gated by the admin middleware that mounts the route.
func (h *SettingsHandler) GetSettings(c *fiber.Ctx) error {
	snap, err := h.settings.Snapshot(c.Context())
	if err != nil {
		log.Error().Err(err).Msg("settings snapshot failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "snapshot failed"})
	}
	return c.JSON(snap)
}

// SetAIDispatchRequest carries the new boolean state.
type SetAIDispatchRequest struct {
	Enabled bool `json:"enabled"`
}

// SetAIDispatch flips the kill-switch. The request body must include
// {"enabled": true|false}. We require the actor (admin claims) so the
// audit row in system_settings.updated_by reflects who toggled it.
func (h *SettingsHandler) SetAIDispatch(c *fiber.Ctx) error {
	claims, ok := c.Locals(middleware.CtxKeyAdminClaims).(*auth.Claims)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "missing claims"})
	}
	var req SetAIDispatchRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.settings.SetAIDispatchEnabled(c.Context(), req.Enabled, claims.UserID); err != nil {
		log.Error().Err(err).Bool("enabled", req.Enabled).Msg("set ai_dispatch_enabled failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "set failed"})
	}
	log.Info().
		Bool("enabled", req.Enabled).
		Str("admin", claims.UserID.String()).
		Msg("ai_dispatch_enabled toggled")
	return c.JSON(fiber.Map{"ai_dispatch_enabled": req.Enabled})
}
