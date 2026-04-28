package middleware

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/worktrack/backend/internal/auth"
)

const (
	HeaderAuthorization = "Authorization"
	CtxKeyAdminClaims   = "admin_claims"
)

// AdminAuth validates the Bearer access token and stores claims in context.
// Reads token from Authorization header; the dashboard sends it in fetch
// while the refresh token stays in an HTTP-only cookie.
func AdminAuth(jwt *auth.JWTIssuer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get(HeaderAuthorization)
		if header == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "missing authorization header"})
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid authorization scheme"})
		}

		claims, err := jwt.Parse(parts[1])
		if err != nil {
			if errors.Is(err, auth.ErrInvalidToken) {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid or expired token"})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "auth check failed"})
		}

		c.Locals(CtxKeyAdminClaims, claims)
		return c.Next()
	}
}

// RequireRole guards endpoints that need elevated privileges (e.g. only "admin"
// can create commands; "viewer" can only read).
func RequireRole(roles ...string) fiber.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals(CtxKeyAdminClaims).(*auth.Claims)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "missing claims"})
		}
		if _, ok := allowed[claims.Role]; !ok {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "insufficient role"})
		}
		return c.Next()
	}
}
