package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/models"
)

// VideoService is the dashboard-side of the onboarding video flow.
// Bytes live on a CDN (admin uploads out-of-band); we only persist
// the URL + SHA256 + size + version label and decide which row is
// active at any moment.
//
// Activation has a side-effect every other publish path doesn't:
// it clears machines.video_played_at across the entire fleet. So
// when the admin swaps from "video v1" to "video v2", every machine
// — even ones that already played v1 — gets the play_video=true
// flag again on its next heartbeat and shows v2 to the employee.
// The clear runs in the same transaction as the is_active flip so
// the fan-out is atomic.
type VideoService struct {
	db *database.DB
}

func NewVideoService(db *database.DB) *VideoService {
	return &VideoService{db: db}
}

var ErrVideoNotFound = errors.New("video not found")

func (s *VideoService) RegisterExternal(ctx context.Context, req models.RegisterExternalVideoRequest, uploadedBy uuid.UUID) (*models.Video, error) {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if req.SetActive {
		if _, err := tx.Exec(ctx, `UPDATE videos SET is_active = FALSE WHERE is_active = TRUE`); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE machines SET video_played_at = NULL WHERE disabled_at IS NULL`); err != nil {
			return nil, err
		}
	}

	v := &models.Video{}
	err = tx.QueryRow(ctx, `
		INSERT INTO videos (filename, sha256, size_bytes, version_label, notes, external_url, uploaded_by, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, filename, sha256, size_bytes, version_label, notes, external_url, uploaded_by, uploaded_at, is_active, revoked_at
	`, req.Filename, req.SHA256, req.SizeBytes, req.VersionLabel, req.Notes, req.URL, uploadedBy, req.SetActive).Scan(
		&v.ID, &v.Filename, &v.SHA256, &v.SizeBytes, &v.VersionLabel, &v.Notes, &v.ExternalURL,
		&v.UploadedBy, &v.UploadedAt, &v.IsActive, &v.RevokedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert video: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *VideoService) List(ctx context.Context) ([]models.Video, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, filename, sha256, size_bytes, version_label, notes, external_url,
		       uploaded_by, uploaded_at, is_active, revoked_at
		FROM videos
		ORDER BY uploaded_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.Video, 0)
	for rows.Next() {
		var v models.Video
		if err := rows.Scan(&v.ID, &v.Filename, &v.SHA256, &v.SizeBytes, &v.VersionLabel,
			&v.Notes, &v.ExternalURL, &v.UploadedBy, &v.UploadedAt, &v.IsActive, &v.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetActive flips one row to is_active=TRUE inside a transaction
// that also clears machines.video_played_at fleet-wide. Same atomicity
// guarantee as the equivalent AI-package path.
func (s *VideoService) SetActive(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE videos SET is_active = FALSE WHERE is_active = TRUE`); err != nil {
		return err
	}

	ct, err := tx.Exec(ctx, `
		UPDATE videos SET is_active = TRUE
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrVideoNotFound
	}

	if _, err := tx.Exec(ctx, `UPDATE machines SET video_played_at = NULL WHERE disabled_at IS NULL`); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *VideoService) Revoke(ctx context.Context, id uuid.UUID) error {
	ct, err := s.db.Pool.Exec(ctx, `
		UPDATE videos SET revoked_at = NOW(), is_active = FALSE
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrVideoNotFound
	}
	return nil
}

// GetActiveForAgent is the heartbeat-time lookup. Returns Available=
// false when no active row exists; otherwise the URL/SHA/size the
// agent needs to download and play.
func (s *VideoService) GetActiveForAgent(ctx context.Context) (*models.AgentVideoResponse, error) {
	var v models.Video
	err := s.db.Pool.QueryRow(ctx, `
		SELECT sha256, size_bytes, version_label, external_url
		FROM videos
		WHERE is_active = TRUE AND revoked_at IS NULL
		LIMIT 1
	`).Scan(&v.SHA256, &v.SizeBytes, &v.VersionLabel, &v.ExternalURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return &models.AgentVideoResponse{Available: false}, nil
	}
	if err != nil {
		return nil, err
	}
	return &models.AgentVideoResponse{
		Available:    true,
		SHA256:       v.SHA256,
		SizeBytes:    v.SizeBytes,
		VersionLabel: v.VersionLabel,
		DownloadURL:  v.ExternalURL,
	}, nil
}
