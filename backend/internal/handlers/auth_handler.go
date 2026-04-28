package handlers

import (
	"errors"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/services"
)

const (
	cookieRefreshToken = "wt_refresh"
)

type AuthHandler struct {
	auth      *services.AuthService
	validator *validator.Validate
	secure    bool
}

func NewAuthHandler(authSvc *services.AuthService, secureCookies bool) *AuthHandler {
	return &AuthHandler{
		auth:      authSvc,
		validator: validator.New(validator.WithRequiredStructEnabled()),
		secure:    secureCookies,
	}
}

type loginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
}

type loginResponse struct {
	AccessToken string         `json:"access_token"`
	ExpiresIn   int            `json:"expires_in"`
	User        userPayload    `json:"user"`
}

type userPayload struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.validator.Struct(req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	res, err := h.auth.Login(c.Context(), req.Email, req.Password, c.Get("User-Agent"), c.IP())
	if err != nil {
		if errors.Is(err, services.ErrInvalidCredentials) || errors.Is(err, services.ErrAccountDisabled) {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid credentials"})
		}
		log.Error().Err(err).Msg("login failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "login failed"})
	}

	h.setRefreshCookie(c, res.RefreshToken, res.RefreshExpiresAt)

	return c.JSON(loginResponse{
		AccessToken: res.AccessToken,
		ExpiresIn:   int(time.Until(res.RefreshExpiresAt).Seconds()),
		User: userPayload{
			ID:    res.User.ID.String(),
			Email: res.User.Email,
			Name:  res.User.Name,
			Role:  res.User.Role,
		},
	})
}

func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	token := c.Cookies(cookieRefreshToken)
	if token == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no refresh token"})
	}

	access, err := h.auth.Refresh(c.Context(), token)
	if err != nil {
		if errors.Is(err, services.ErrSessionInvalid) {
			h.clearRefreshCookie(c)
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid session"})
		}
		log.Error().Err(err).Msg("refresh failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "refresh failed"})
	}
	return c.JSON(fiber.Map{"access_token": access})
}

func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	token := c.Cookies(cookieRefreshToken)
	if token != "" {
		_ = h.auth.Logout(c.Context(), token)
	}
	h.clearRefreshCookie(c)
	return c.JSON(fiber.Map{"ok": true})
}

func (h *AuthHandler) setRefreshCookie(c *fiber.Ctx, token string, expires time.Time) {
	c.Cookie(&fiber.Cookie{
		Name:     cookieRefreshToken,
		Value:    token,
		Path:     "/api/v1/auth",
		Expires:  expires,
		HTTPOnly: true,
		Secure:   h.secure,
		SameSite: "Strict",
	})
}

func (h *AuthHandler) clearRefreshCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     cookieRefreshToken,
		Value:    "",
		Path:     "/api/v1/auth",
		Expires:  time.Now().Add(-time.Hour),
		HTTPOnly: true,
		Secure:   h.secure,
		SameSite: "Strict",
	})
}
