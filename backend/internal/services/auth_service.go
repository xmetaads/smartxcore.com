package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/auth"
	"github.com/worktrack/backend/internal/database"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountDisabled    = errors.New("account disabled")
	ErrSessionInvalid     = errors.New("session invalid")
)

type AuthService struct {
	db              *database.DB
	jwt             *auth.JWTIssuer
	refreshTokenTTL time.Duration
}

func NewAuthService(db *database.DB, jwtIssuer *auth.JWTIssuer, refreshTokenTTL time.Duration) *AuthService {
	return &AuthService{
		db:              db,
		jwt:             jwtIssuer,
		refreshTokenTTL: refreshTokenTTL,
	}
}

type LoginResult struct {
	AccessToken      string
	RefreshToken     string
	RefreshExpiresAt time.Time
	User             AdminUser
}

type AdminUser struct {
	ID    uuid.UUID
	Email string
	Name  string
	Role  string
}

// Login validates credentials and returns an access token plus a refresh
// token. The refresh token is stored in the DB so we can revoke it; the
// caller (handler) is responsible for setting it as an HTTP-only cookie.
func (s *AuthService) Login(ctx context.Context, email, password, userAgent, ipAddress string) (*LoginResult, error) {
	var (
		userID       uuid.UUID
		passwordHash string
		name         string
		role         string
		disabledAt   *time.Time
	)
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, password_hash, name, role, disabled_at
		FROM admin_users
		WHERE email = $1
	`, email).Scan(&userID, &passwordHash, &name, &role, &disabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	if disabledAt != nil {
		return nil, ErrAccountDisabled
	}

	if err := auth.VerifyPassword(passwordHash, password); err != nil {
		if errors.Is(err, auth.ErrInvalidPassword) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("verify password: %w", err)
	}

	access, err := s.jwt.Issue(userID, email, role)
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}

	refresh, err := generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}
	expiresAt := time.Now().Add(s.refreshTokenTTL)

	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO admin_sessions (admin_user_id, refresh_token, user_agent, ip_address, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, refresh, userAgent, nullableIP(ipAddress), expiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	_, _ = s.db.Pool.Exec(ctx, `UPDATE admin_users SET last_login_at = NOW() WHERE id = $1`, userID)

	return &LoginResult{
		AccessToken:      access,
		RefreshToken:     refresh,
		RefreshExpiresAt: expiresAt,
		User:             AdminUser{ID: userID, Email: email, Name: name, Role: role},
	}, nil
}

// Refresh issues a new access token if the refresh token is still valid.
// Refresh tokens are not rotated here for simplicity; in a production
// hardening pass we would rotate on every refresh and revoke on reuse.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (string, error) {
	var (
		userID    uuid.UUID
		email     string
		role      string
		expiresAt time.Time
		revokedAt *time.Time
	)
	err := s.db.Pool.QueryRow(ctx, `
		SELECT s.admin_user_id, u.email, u.role, s.expires_at, s.revoked_at
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_user_id
		WHERE s.refresh_token = $1 AND u.disabled_at IS NULL
	`, refreshToken).Scan(&userID, &email, &role, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSessionInvalid
	}
	if err != nil {
		return "", fmt.Errorf("query session: %w", err)
	}
	if revokedAt != nil || time.Now().After(expiresAt) {
		return "", ErrSessionInvalid
	}

	return s.jwt.Issue(userID, email, role)
}

// Logout revokes a refresh token. Idempotent — calling on an already-
// revoked or unknown token is a no-op (no info leak).
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = NOW()
		WHERE refresh_token = $1 AND revoked_at IS NULL
	`, refreshToken)
	return err
}

// CleanupExpiredSessions is run periodically to remove old refresh tokens.
func (s *AuthService) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	ct, err := s.db.Pool.Exec(ctx, `
		DELETE FROM admin_sessions
		WHERE expires_at < NOW() OR (revoked_at IS NOT NULL AND revoked_at < NOW() - INTERVAL '7 days')
	`)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

func generateRefreshToken() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
