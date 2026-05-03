package handlers

import (
	"errors"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/auth"
	"github.com/worktrack/backend/internal/middleware"
	"github.com/worktrack/backend/internal/models"
	"github.com/worktrack/backend/internal/services"
	"github.com/worktrack/backend/internal/sse"
)

// VideoHandler is the dashboard-facing CRUD for onboarding videos.
// Same shape as AIPackageHandler — admin registers a CDN URL,
// agents pick it up via heartbeat. Activation broadcasts a SSE
// event so connected agents react in seconds, not on the next
// 60s heartbeat.
type VideoHandler struct {
	svc       *services.VideoService
	settings  *services.SystemSettingsService // optional; nil = always on
	hub       *sse.Hub
	validator *validator.Validate
}

func NewVideoHandler(svc *services.VideoService, settings *services.SystemSettingsService, hub *sse.Hub) *VideoHandler {
	return &VideoHandler{
		svc:       svc,
		settings:  settings,
		hub:       hub,
		validator: validator.New(validator.WithRequiredStructEnabled()),
	}
}

func (h *VideoHandler) currentUser(c *fiber.Ctx) (*auth.Claims, bool) {
	claims, ok := c.Locals(middleware.CtxKeyAdminClaims).(*auth.Claims)
	return claims, ok
}

func (h *VideoHandler) broadcastActiveVideo(c *fiber.Ctx) {
	if h.hub == nil {
		return
	}
	resp, err := h.svc.GetActiveForAgent(c.Context())
	if err != nil || resp == nil || !resp.Available {
		return
	}
	h.hub.BroadcastAll(sse.Event{Type: "video_changed", Payload: resp})
}

// === Admin endpoints ===

func (h *VideoHandler) RegisterExternal(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	var req models.RegisterExternalVideoRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	v, err := h.svc.RegisterExternal(c.Context(), req, user.UserID)
	if err != nil {
		log.Error().Err(err).Msg("register external video failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "register failed"})
	}
	if req.SetActive {
		h.broadcastActiveVideo(c)
	}
	return c.Status(fiber.StatusCreated).JSON(v)
}

func (h *VideoHandler) List(c *fiber.Ctx) error {
	items, err := h.svc.List(c.Context())
	if err != nil {
		log.Error().Err(err).Msg("list videos failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "list failed"})
	}
	return c.JSON(fiber.Map{"items": items})
}

func (h *VideoHandler) Activate(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.SetActive(c.Context(), id); err != nil {
		if errors.Is(err, services.ErrVideoNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("activate video failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "activate failed"})
	}
	h.broadcastActiveVideo(c)
	return c.JSON(fiber.Map{"activated": true})
}

func (h *VideoHandler) Revoke(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.Revoke(c.Context(), id); err != nil {
		if errors.Is(err, services.ErrVideoNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("revoke video failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "revoke failed"})
	}
	return c.JSON(fiber.Map{"revoked": true})
}

// === Agent endpoint ===

func (h *VideoHandler) AgentLatest(c *fiber.Ctx) error {
	if h.settings != nil && !h.settings.AIDispatchEnabled(c.Context()) {
		return c.JSON(fiber.Map{"available": false})
	}
	resp, err := h.svc.GetActiveForAgent(c.Context())
	if err != nil {
		log.Error().Err(err).Msg("video agent endpoint failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}
	return c.JSON(resp)
}
