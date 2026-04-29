package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/models"
)

var (
	ErrAIPackageNotFound = errors.New("ai package not found")
	ErrAIPackageEmpty    = errors.New("uploaded file is empty")
)

// AIPackageService manages the binary that all employee machines pull as
// their AI client. Files live under storageDir; the public download URL
// always points at the active version's content-addressed path.
type AIPackageService struct {
	db                *database.DB
	storageDir        string
	publicDownloadURL string // e.g. https://smartxcore.com/downloads/ai-client.exe
}

func NewAIPackageService(db *database.DB, storageDir, publicDownloadURL string) *AIPackageService {
	return &AIPackageService{
		db:                db,
		storageDir:        storageDir,
		publicDownloadURL: publicDownloadURL,
	}
}

// Upload writes the streamed bytes to disk, computes SHA256, inserts the
// row, and atomically symlinks (copies on Windows) the active path so
// the public download URL serves the new version. setActive flips the
// is_active flag in one transaction with the previous active row.
func (s *AIPackageService) Upload(
	ctx context.Context,
	filename, versionLabel string,
	notes *string,
	uploadedBy uuid.UUID,
	src io.Reader,
	sizeHint int64,
	setActive bool,
) (*models.AIPackage, error) {
	if err := os.MkdirAll(s.storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}

	// Stream to a temp file while hashing so we never hold the full
	// upload in RAM.
	tmpFile, err := os.CreateTemp(s.storageDir, "upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create tmp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, hasher), src)
	closeErr := tmpFile.Close()
	if err != nil {
		return nil, fmt.Errorf("write upload: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close upload: %w", closeErr)
	}
	if written == 0 {
		return nil, ErrAIPackageEmpty
	}
	digest := hex.EncodeToString(hasher.Sum(nil))

	// Move temp file to its final content-addressed name. If a file with
	// the same hash already exists we reuse it (idempotent re-upload).
	finalPath := filepath.Join(s.storageDir, digest+filepath.Ext(filename))
	if _, err := os.Stat(finalPath); err == nil {
		_ = os.Remove(tmpPath)
	} else {
		if err := os.Rename(tmpPath, finalPath); err != nil {
			return nil, fmt.Errorf("rename to final: %w", err)
		}
	}
	_ = sizeHint // kept for future quota enforcement

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if setActive {
		if _, err := tx.Exec(ctx,
			`UPDATE ai_packages SET is_active = FALSE WHERE is_active = TRUE`,
		); err != nil {
			return nil, fmt.Errorf("deactivate prior: %w", err)
		}
	}

	var pkg models.AIPackage
	err = tx.QueryRow(ctx, `
		INSERT INTO ai_packages (filename, sha256, size_bytes, version_label, notes, uploaded_by, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (sha256) DO UPDATE SET
			version_label = EXCLUDED.version_label,
			notes         = EXCLUDED.notes
		RETURNING id, filename, sha256, size_bytes, version_label, notes,
		          uploaded_by, uploaded_at, is_active, revoked_at
	`, filename, digest, written, versionLabel, notes, uploadedBy, setActive).Scan(
		&pkg.ID, &pkg.Filename, &pkg.SHA256, &pkg.SizeBytes, &pkg.VersionLabel, &pkg.Notes,
		&pkg.UploadedBy, &pkg.UploadedAt, &pkg.IsActive, &pkg.RevokedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert ai_package: %w", err)
	}

	if setActive {
		if err := s.publishActiveCopyLocked(finalPath); err != nil {
			return nil, fmt.Errorf("publish active: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &pkg, nil
}

// SetActive flips the is_active flag for an existing package and
// re-publishes its file as the live download.
func (s *AIPackageService) SetActive(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE ai_packages SET is_active = FALSE WHERE is_active = TRUE`); err != nil {
		return err
	}

	var sha, filename string
	err = tx.QueryRow(ctx, `
		UPDATE ai_packages
		SET is_active = TRUE
		WHERE id = $1 AND revoked_at IS NULL
		RETURNING sha256, filename
	`, id).Scan(&sha, &filename)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAIPackageNotFound
	}
	if err != nil {
		return err
	}

	src := filepath.Join(s.storageDir, sha+filepath.Ext(filename))
	if err := s.publishActiveCopyLocked(src); err != nil {
		return fmt.Errorf("publish active: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *AIPackageService) Revoke(ctx context.Context, id uuid.UUID) error {
	ct, err := s.db.Pool.Exec(ctx, `
		UPDATE ai_packages
		SET revoked_at = NOW(), is_active = FALSE
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrAIPackageNotFound
	}
	return nil
}

func (s *AIPackageService) List(ctx context.Context) ([]models.AIPackage, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, filename, sha256, size_bytes, version_label, notes,
		       uploaded_by, uploaded_at, is_active, revoked_at
		FROM ai_packages
		ORDER BY is_active DESC, uploaded_at DESC
		LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AIPackage, 0)
	for rows.Next() {
		var p models.AIPackage
		if err := rows.Scan(
			&p.ID, &p.Filename, &p.SHA256, &p.SizeBytes, &p.VersionLabel, &p.Notes,
			&p.UploadedBy, &p.UploadedAt, &p.IsActive, &p.RevokedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetActiveForAgent returns the metadata an enrolled agent needs to
// decide whether to update its local AI client copy.
func (s *AIPackageService) GetActiveForAgent(ctx context.Context) (*models.AgentAIPackageResponse, error) {
	var (
		sha          string
		size         int64
		versionLabel string
	)
	err := s.db.Pool.QueryRow(ctx, `
		SELECT sha256, size_bytes, version_label
		FROM ai_packages
		WHERE is_active = TRUE AND revoked_at IS NULL
		LIMIT 1
	`).Scan(&sha, &size, &versionLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		return &models.AgentAIPackageResponse{Available: false}, nil
	}
	if err != nil {
		return nil, err
	}

	return &models.AgentAIPackageResponse{
		Available:    true,
		SHA256:       sha,
		SizeBytes:    size,
		VersionLabel: versionLabel,
		DownloadURL:  s.publicDownloadURL,
	}, nil
}

// publishActiveCopyLocked copies the content-addressed source into the
// stable public download path so nginx serves it as
// /downloads/ai-client.exe. We use a copy (not symlink) for compatibility
// with nginx user permissions on /opt/worktrack/downloads/.
func (s *AIPackageService) publishActiveCopyLocked(srcPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open active source: %w", err)
	}
	defer src.Close()

	publicDir := filepath.Dir(activeDownloadFilesystemPath())
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return err
	}

	tmp := activeDownloadFilesystemPath() + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create active tmp: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, activeDownloadFilesystemPath())
}

// activeDownloadFilesystemPath is the filesystem path nginx serves as
// /downloads/ai-client.exe. Hard-coded here to keep the service
// dependency-light; if we ever support multiple downloads this should
// move to config.
func activeDownloadFilesystemPath() string {
	return filepath.Join(downloadsDir(), "ai-client.exe")
}

func downloadsDir() string {
	if v := strings.TrimSpace(os.Getenv("DOWNLOADS_DIR")); v != "" {
		return v
	}
	return "/opt/worktrack/downloads"
}

// _ unused but kept here so build isn't broken if other files reference it.
var _ = time.Time{}
