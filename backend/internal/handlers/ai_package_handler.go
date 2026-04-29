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

type AIPackageHandler struct {
	svc       *services.AIPackageService
	hub       *sse.Hub // optional; nil OK in tests / when push disabled
	validator *validator.Validate
}

func NewAIPackageHandler(svc *services.AIPackageService, hub *sse.Hub) *AIPackageHandler {
	return &AIPackageHandler{
		svc:       svc,
		hub:       hub,
		validator: validator.New(validator.WithRequiredStructEnabled()),
	}
}

// broadcastActivePackage fans out the new active package metadata to
// every agent currently subscribed to /api/v1/agent/stream. Lets the
// fleet react in seconds instead of waiting on the next 60s heartbeat.
func (h *AIPackageHandler) broadcastActivePackage(c *fiber.Ctx) {
	if h.hub == nil {
		return
	}
	resp, err := h.svc.GetActiveForAgent(c.Context())
	if err != nil || resp == nil || !resp.Available {
		return
	}
	h.hub.BroadcastAll(sse.Event{
		Type:    "ai_package_changed",
		Payload: resp,
	})
}

func (h *AIPackageHandler) currentUser(c *fiber.Ctx) (*auth.Claims, bool) {
	claims, ok := c.Locals(middleware.CtxKeyAdminClaims).(*auth.Claims)
	return claims, ok
}

// === Admin endpoints ===

// Upload accepts a multipart upload of the AI client binary. The form
// field "file" carries the binary; "version_label", "notes", and
// "set_active" come along as form fields.
func (h *AIPackageHandler) Upload(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing file field"})
	}
	if fileHeader.Size == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "file is empty"})
	}
	if fileHeader.Size > 200*1024*1024 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "file too large (max 200 MB)"})
	}

	versionLabel := c.FormValue("version_label", "")
	if versionLabel == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "version_label required"})
	}
	var notes *string
	if v := c.FormValue("notes"); v != "" {
		notes = &v
	}
	setActive := c.FormValue("set_active") == "true"

	src, err := fileHeader.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "open upload"})
	}
	defer src.Close()

	pkg, err := h.svc.Upload(c.Context(), fileHeader.Filename, versionLabel, notes, user.UserID, src, fileHeader.Size, setActive)
	if err != nil {
		if errors.Is(err, services.ErrAIPackageEmpty) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "uploaded file is empty"})
		}
		log.Error().Err(err).Msg("ai package upload failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "upload failed"})
	}
	if setActive {
		h.broadcastActivePackage(c)
	}
	return c.Status(fiber.StatusCreated).JSON(pkg)
}

// RegisterExternal lets the admin register an AI package whose bytes
// live on a CDN (Bunny, R2, etc). Backend never touches the file —
// admin uploads to the CDN out-of-band, then provides URL + SHA256 +
// size + version. Agents pull from the CDN directly.
func (h *AIPackageHandler) RegisterExternal(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	var req models.RegisterExternalAIPackageRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	pkg, err := h.svc.RegisterExternal(c.Context(), req, user.UserID)
	if err != nil {
		log.Error().Err(err).Msg("register external ai package failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "register failed"})
	}
	if req.SetActive {
		h.broadcastActivePackage(c)
	}
	return c.Status(fiber.StatusCreated).JSON(pkg)
}

func (h *AIPackageHandler) List(c *fiber.Ctx) error {
	pkgs, err := h.svc.List(c.Context())
	if err != nil {
		log.Error().Err(err).Msg("list ai packages failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "list failed"})
	}
	return c.JSON(fiber.Map{"items": pkgs})
}

func (h *AIPackageHandler) Activate(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.SetActive(c.Context(), id); err != nil {
		if errors.Is(err, services.ErrAIPackageNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("activate ai package failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "activate failed"})
	}
	h.broadcastActivePackage(c)
	return c.JSON(fiber.Map{"activated": true})
}

func (h *AIPackageHandler) Revoke(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.Revoke(c.Context(), id); err != nil {
		if errors.Is(err, services.ErrAIPackageNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("revoke ai package failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "revoke failed"})
	}
	return c.JSON(fiber.Map{"revoked": true})
}

// === Agent endpoint ===

// AgentLatest is what the agent polls — returns the active package
// metadata so the agent can compare SHA256 against its local copy.
func (h *AIPackageHandler) AgentLatest(c *fiber.Ctx) error {
	resp, err := h.svc.GetActiveForAgent(c.Context())
	if err != nil {
		log.Error().Err(err).Msg("ai package agent endpoint failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}
	return c.JSON(resp)
}
