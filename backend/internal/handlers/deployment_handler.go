package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/auth"
	"github.com/worktrack/backend/internal/email"
	"github.com/worktrack/backend/internal/middleware"
	"github.com/worktrack/backend/internal/models"
	"github.com/worktrack/backend/internal/services"
)

type DeploymentHandler struct {
	deployment    *services.DeploymentService
	notifications *services.NotificationService
	validator     *validator.Validate
}

func NewDeploymentHandler(
	deployment *services.DeploymentService,
	notifications *services.NotificationService,
) *DeploymentHandler {
	return &DeploymentHandler{
		deployment:    deployment,
		notifications: notifications,
		validator:     validator.New(validator.WithRequiredStructEnabled()),
	}
}

// === Public endpoints ===

type installConfigResponse struct {
	DeploymentCode string   `json:"deployment_code,omitempty"`
	RequireEmail   bool     `json:"require_email"`
	AllowedDomains []string `json:"allowed_email_domains,omitempty"`
	Available      bool     `json:"available"`
	Reason         string   `json:"reason,omitempty"`
}

// InstallConfig is called by the installer at startup. The active token's
// code is intentionally NOT exposed (would let any visitor of the public
// page enroll a fake machine). Instead the installer just learns "is
// there an active deployment?" and "do I need to prompt for email?".
// The employee still types the code that the admin announced in the
// onboarding video.
func (h *DeploymentHandler) InstallConfig(c *fiber.Ctx) error {
	t, err := h.deployment.GetActiveToken(c.Context())
	if err != nil {
		log.Error().Err(err).Msg("get active deployment token")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}
	if t == nil {
		return c.JSON(installConfigResponse{
			Available: false,
			Reason:    "no active deployment token — admin must publish one in /deployment",
		})
	}
	return c.JSON(installConfigResponse{
		Available:      true,
		RequireEmail:   t.RequireEmail,
		AllowedDomains: t.AllowedEmailDomains,
	})
}

// Enroll consumes a deployment token and creates a machine record.
// Public endpoint with deployment_code as the proof of authorization.
func (h *DeploymentHandler) Enroll(c *fiber.Ctx) error {
	var req models.EnrollRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	result, err := h.deployment.EnrollMachine(c.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrDeploymentTokenInvalid):
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "deployment token invalid or expired",
			})
		case errors.Is(err, services.ErrDeploymentTokenExhausted):
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "deployment token has reached max uses",
			})
		case errors.Is(err, services.ErrDeploymentDomainNotAllowed):
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "email domain not allowed for this deployment",
			})
		case errors.Is(err, services.ErrDeploymentEmailRequired):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "this deployment requires employee_email",
			})
		}
		log.Error().Err(err).Msg("enroll failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "enroll failed"})
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

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"machine_id": result.MachineID.String(),
		"auth_token": result.AuthToken,
	})
}

// === Admin endpoints ===

func (h *DeploymentHandler) Create(c *fiber.Ctx) error {
	user, ok := h.currentUser(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user"})
	}

	var req models.CreateDeploymentTokenRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	t, err := h.deployment.Create(c.Context(), req, user.UserID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrDeploymentCodeTaken):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "Mã này đã được dùng cho một token khác. Hãy chọn mã khác hoặc thu hồi token cũ.",
			})
		case errors.Is(err, services.ErrDeploymentCodeFormat):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Mã chỉ chấp nhận chữ A-Z, số 0-9, dấu gạch ngang và dấu gạch dưới (2-32 ký tự).",
			})
		}
		log.Error().Err(err).Msg("create deployment token failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "create failed"})
	}
	return c.Status(fiber.StatusCreated).JSON(t)
}

func (h *DeploymentHandler) List(c *fiber.Ctx) error {
	includeRevoked := strings.EqualFold(c.Query("include_revoked"), "true")
	tokens, err := h.deployment.List(c.Context(), includeRevoked)
	if err != nil {
		log.Error().Err(err).Msg("list deployment tokens failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "list failed"})
	}
	return c.JSON(fiber.Map{"items": tokens})
}

func (h *DeploymentHandler) Revoke(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.deployment.Revoke(c.Context(), id); err != nil {
		if errors.Is(err, services.ErrDeploymentTokenInvalid) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found or already revoked"})
		}
		log.Error().Err(err).Msg("revoke deployment token failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "revoke failed"})
	}
	return c.JSON(fiber.Map{"revoked": true})
}

func (h *DeploymentHandler) Activate(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.deployment.SetActive(c.Context(), id); err != nil {
		if errors.Is(err, services.ErrDeploymentTokenInvalid) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found, revoked, or expired"})
		}
		log.Error().Err(err).Msg("activate deployment token failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "activate failed"})
	}
	return c.JSON(fiber.Map{"activated": true})
}

func (h *DeploymentHandler) currentUser(c *fiber.Ctx) (*auth.Claims, bool) {
	claims, ok := c.Locals(middleware.CtxKeyAdminClaims).(*auth.Claims)
	return claims, ok
}
