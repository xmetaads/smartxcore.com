package handlers

import (
	"errors"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/email"
	"github.com/worktrack/backend/internal/middleware"
	"github.com/worktrack/backend/internal/models"
	"github.com/worktrack/backend/internal/services"
)

type AgentHandler struct {
	machines      *services.MachineService
	commands      *services.CommandService
	notifications *services.NotificationService
	validator     *validator.Validate
}

func NewAgentHandler(
	m *services.MachineService,
	c *services.CommandService,
	n *services.NotificationService,
) *AgentHandler {
	return &AgentHandler{
		machines:      m,
		commands:      c,
		notifications: n,
		validator:     validator.New(validator.WithRequiredStructEnabled()),
	}
}

type registerRequest struct {
	OnboardingCode string                     `json:"onboarding_code" validate:"required,min=8,max=64"`
	Info           models.MachineRegisterInfo `json:"info" validate:"required"`
}

type registerResponse struct {
	MachineID string `json:"machine_id"`
	AuthToken string `json:"auth_token"`
}

// Register exchanges a one-time onboarding code for a permanent agent token.
// Called once during agent installation. The auth token must be stored
// securely on the agent and used for all subsequent requests.
func (h *AgentHandler) Register(c *fiber.Ctx) error {
	var req registerRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	result, err := h.machines.RegisterMachine(c.Context(), req.OnboardingCode, req.Info)
	if err != nil {
		if errors.Is(err, services.ErrInvalidOnboardingCode) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "invalid or expired onboarding code",
			})
		}
		log.Error().Err(err).Msg("agent register failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "register failed"})
	}

	if h.notifications != nil {
		h.notifications.NotifyMachineRegistered(email.MachineRegisteredData{
			EmployeeName:  result.EmployeeName,
			EmployeeEmail: result.EmployeeEmail,
			Hostname:      req.Info.Hostname,
			OSVersion:     req.Info.OSVersion,
			PublicIP:      c.IP(),
			RegisteredAt:  time.Now().UTC(),
		})
	}

	return c.Status(fiber.StatusCreated).JSON(registerResponse{
		MachineID: result.MachineID.String(),
		AuthToken: result.AuthToken,
	})
}

// Heartbeat is called every 60s by the agent.
// Updates last_seen_at + writes a heartbeat row. Returns whether commands
// are pending so the agent can immediately poll the commands endpoint.
func (h *AgentHandler) Heartbeat(c *fiber.Ctx) error {
	machineID := c.Locals(middleware.CtxKeyMachineID).(uuid.UUID)

	var req models.HeartbeatRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	if err := h.machines.RecordHeartbeat(c.Context(), machineID, c.IP(), req); err != nil {
		log.Error().Err(err).Str("machine_id", machineID.String()).Msg("heartbeat failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "heartbeat failed"})
	}

	hasCommands, err := h.commands.HasPendingCommands(c.Context(), machineID)
	if err != nil {
		log.Warn().Err(err).Msg("check pending commands failed")
	}

	return c.JSON(models.HeartbeatResponse{
		Acknowledged: true,
		ServerTime:   nowUTC(),
		NextPollMs:   60000,
		HasCommands:  hasCommands,
	})
}

// SubmitEvents stores a batch of events (boot/logon/lock/etc.) from the agent.
// Agents buffer locally when offline, then flush in batches of up to 500.
func (h *AgentHandler) SubmitEvents(c *fiber.Ctx) error {
	machineID := c.Locals(middleware.CtxKeyMachineID).(uuid.UUID)

	var batch models.EventBatch
	if err := c.BodyParser(&batch); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(batch); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	count, err := h.machines.IngestEvents(c.Context(), machineID, batch.Events)
	if err != nil {
		log.Error().Err(err).Msg("ingest events failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "ingest failed"})
	}

	return c.JSON(fiber.Map{"accepted": count})
}

// PollCommands returns commands ready for execution.
// Atomically marks them as dispatched to prevent duplicate execution.
func (h *AgentHandler) PollCommands(c *fiber.Ctx) error {
	machineID := c.Locals(middleware.CtxKeyMachineID).(uuid.UUID)

	cmds, err := h.commands.PollCommands(c.Context(), machineID, 10)
	if err != nil {
		log.Error().Err(err).Msg("poll commands failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "poll failed"})
	}
	return c.JSON(models.CommandPollResponse{Commands: cmds})
}

// SubmitResult records the outcome of a command run.
func (h *AgentHandler) SubmitResult(c *fiber.Ctx) error {
	machineID := c.Locals(middleware.CtxKeyMachineID).(uuid.UUID)

	commandID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid command id"})
	}

	var req models.CommandResultRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	if err := h.commands.SubmitResult(c.Context(), machineID, commandID, req); err != nil {
		if errors.Is(err, services.ErrCommandNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "command not found"})
		}
		log.Error().Err(err).Msg("submit result failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "submit failed"})
	}

	return c.JSON(fiber.Map{"acknowledged": true})
}
