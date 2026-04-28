package middleware

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/worktrack/backend/internal/services"
)

const (
	HeaderAgentToken = "X-Agent-Token"
	CtxKeyMachineID  = "machine_id"
)

// AgentAuth validates the X-Agent-Token header on every agent request and
// stores the resolved machine ID in the request context.
func AgentAuth(svc *services.MachineService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := c.Get(HeaderAgentToken)
		if token == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "missing agent token",
			})
		}

		machineID, err := svc.AuthenticateAgent(c.Context(), token)
		if err != nil {
			if errors.Is(err, services.ErrInvalidAuthToken) {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"error": "invalid agent token",
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "auth check failed",
			})
		}

		c.Locals(CtxKeyMachineID, machineID)
		return c.Next()
	}
}
