package handlers

import (
	"errors"
	"strconv"

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

type AdminHandler struct {
	admin     *services.AdminService
	commands  *services.CommandService
	hub       *sse.Hub // optional; nil OK in tests
	validator *validator.Validate
}

func NewAdminHandler(admin *services.AdminService, commands *services.CommandService, hub *sse.Hub) *AdminHandler {
	return &AdminHandler{
		admin:     admin,
		commands:  commands,
		hub:       hub,
		validator: validator.New(validator.WithRequiredStructEnabled()),
	}
}

func (h *AdminHandler) currentUser(c *fiber.Ctx) (*auth.Claims, bool) {
	claims, ok := c.Locals(middleware.CtxKeyAdminClaims).(*auth.Claims)
	return claims, ok
}

// === Machines ===

func (h *AdminHandler) ListMachines(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	offlineHours, _ := strconv.Atoi(c.Query("offline_hours", "0"))

	filter := services.MachineListFilter{
		Search:       c.Query("search"),
		OnlineOnly:   c.Query("online") == "true",
		OfflineHours: offlineHours,
		Department:   c.Query("department"),
		Page:         page,
		PageSize:     pageSize,
	}

	res, err := h.admin.ListMachines(c.Context(), filter)
	if err != nil {
		log.Error().Err(err).Msg("list machines failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "list failed"})
	}
	return c.JSON(res)
}

func (h *AdminHandler) GetMachine(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	m, err := h.admin.GetMachine(c.Context(), id)
	if err != nil {
		if errors.Is(err, services.ErrMachineNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("get machine failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "get failed"})
	}
	return c.JSON(m)
}

// DeleteMachine soft-deletes a machine. The agent on that machine keeps
// its auth token valid: if the agent heartbeats again the row is
// auto-restored (disabled_at cleared). This is what makes "I deleted by
// accident" recoverable for both online and offline machines.
func (h *AdminHandler) DeleteMachine(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	err = h.admin.DeleteMachine(c.Context(), id, user.UserID, c.IP(), c.Get("User-Agent"))
	if err != nil {
		if errors.Is(err, services.ErrMachineNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("delete machine failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "delete failed"})
	}
	return c.JSON(fiber.Map{"deleted": true})
}

// === Onboarding tokens ===

type createOnboardingTokenRequest struct {
	EmployeeEmail string  `json:"employee_email" validate:"required,email"`
	EmployeeName  string  `json:"employee_name" validate:"required,min=1,max=200"`
	Department    *string `json:"department,omitempty" validate:"omitempty,max=100"`
	Notes         *string `json:"notes,omitempty" validate:"omitempty,max=1000"`
	TTLHours      int     `json:"ttl_hours" validate:"omitempty,min=1,max=720"`
}

func (h *AdminHandler) CreateOnboardingToken(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	var req createOnboardingTokenRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	tok, err := h.admin.CreateOnboardingToken(c.Context(), services.CreateOnboardingTokenInput{
		EmployeeEmail: req.EmployeeEmail,
		EmployeeName:  req.EmployeeName,
		Department:    req.Department,
		Notes:         req.Notes,
		TTLHours:      req.TTLHours,
		CreatedBy:     user.UserID,
	})
	if err != nil {
		log.Error().Err(err).Msg("create onboarding token failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "create failed"})
	}
	return c.Status(fiber.StatusCreated).JSON(tok)
}

func (h *AdminHandler) ListOnboardingTokens(c *fiber.Ctx) error {
	includeUsed := c.Query("include_used") == "true"
	limit, _ := strconv.Atoi(c.Query("limit", "100"))

	tokens, err := h.admin.ListOnboardingTokens(c.Context(), includeUsed, limit)
	if err != nil {
		log.Error().Err(err).Msg("list tokens failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "list failed"})
	}
	return c.JSON(fiber.Map{"items": tokens})
}

// === Commands ===

func (h *AdminHandler) CreateCommand(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	var req models.CommandCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	ids, err := h.commands.CreateCommands(c.Context(), user.UserID, req)
	if err != nil {
		log.Error().Err(err).Msg("create commands failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "create failed"})
	}

	// Push a "command_pending" wakeup to each target machine over its
	// SSE stream so the executor polls within ~50ms instead of waiting
	// for the next 60s heartbeat. The agent's executor is already
	// idempotent (poll/result is exactly-once on the server) so this
	// is purely a latency optimisation.
	if h.hub != nil {
		for _, machineID := range req.MachineIDs {
			h.hub.SendToMachine(machineID.String(), sse.Event{
				Type:    "command_pending",
				Payload: map[string]any{},
			})
		}
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"command_ids": ids,
		"count":       len(ids),
	})
}

func (h *AdminHandler) GetCommand(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	cmd, err := h.commands.GetCommand(c.Context(), id)
	if err != nil {
		if errors.Is(err, services.ErrCommandNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		log.Error().Err(err).Msg("get command failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "get failed"})
	}
	return c.JSON(cmd)
}
