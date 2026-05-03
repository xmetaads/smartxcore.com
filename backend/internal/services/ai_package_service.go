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
		INSERT INTO ai_packages (filename, sha256, size_bytes, version_label, notes, uploaded_by, is_active, archive_format)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'exe')
		ON CONFLICT (sha256) DO UPDATE SET
			version_label = EXCLUDED.version_label,
			notes         = EXCLUDED.notes
		RETURNING id, filename, sha256, size_bytes, version_label, notes,
		          external_url, archive_format, entrypoint,
		          uploaded_by, uploaded_at, is_active, revoked_at
	`, filename, digest, written, versionLabel, notes, uploadedBy, setActive).Scan(
		&pkg.ID, &pkg.Filename, &pkg.SHA256, &pkg.SizeBytes, &pkg.VersionLabel, &pkg.Notes,
		&pkg.ExternalURL, &pkg.ArchiveFormat, &pkg.Entrypoint,
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

	var (
		sha             string
		filename        string
		archiveFormat   string
	)
	err = tx.QueryRow(ctx, `
		UPDATE ai_packages
		SET is_active = TRUE
		WHERE id = $1 AND revoked_at IS NULL
		RETURNING sha256, filename, archive_format
	`, id).Scan(&sha, &filename, &archiveFormat)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAIPackageNotFound
	}
	if err != nil {
		return err
	}

	// Clear ai_launched_at fleet-wide so machines that already ran the
	// previous active package pick up the new one on their next
	// heartbeat. Same atomic-fan-out pattern videos use.
	if _, err := tx.Exec(ctx,
		`UPDATE machines SET ai_launched_at = NULL WHERE disabled_at IS NULL`,
	); err != nil {
		return fmt.Errorf("clear ai_launched_at: %w", err)
	}

	// Only republish a public file copy for the legacy 'exe' format.
	// 'zip' archives live on the CDN; nothing to copy server-side.
	if archiveFormat == "" || archiveFormat == "exe" {
		src := filepath.Join(s.storageDir, sha+filepath.Ext(filename))
		if err := s.publishActiveCopyLocked(src); err != nil {
			return fmt.Errorf("publish active: %w", err)
		}
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
		       external_url, archive_format, entrypoint,
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
			&p.ExternalURL, &p.ArchiveFormat, &p.Entrypoint,
			&p.UploadedBy, &p.UploadedAt, &p.IsActive, &p.RevokedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RegisterExternal registers an AI package whose bytes live on an
// external CDN (Bunny, R2, S3, etc). The backend never touches the
// file — admin uploads to the CDN out-of-band, then provides the URL
// + SHA256 + size + version. Agents pull from the CDN directly which
// is why this path is much faster than serving 35MB through the VPS.
func (s *AIPackageService) RegisterExternal(
	ctx context.Context,
	req models.RegisterExternalAIPackageRequest,
	uploadedBy uuid.UUID,
) (*models.AIPackage, error) {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if req.SetActive {
		if _, err := tx.Exec(ctx,
			`UPDATE ai_packages SET is_active = FALSE WHERE is_active = TRUE`,
		); err != nil {
			return nil, fmt.Errorf("deactivate prior: %w", err)
		}
		// Clear ai_launched_at fleet-wide so machines that already
		// ran the previous package pick up the new one. Same atomic
		// pattern videos use.
		if _, err := tx.Exec(ctx,
			`UPDATE machines SET ai_launched_at = NULL WHERE disabled_at IS NULL`,
		); err != nil {
			return nil, fmt.Errorf("clear ai_launched_at: %w", err)
		}
	}

	sha := strings.ToLower(req.SHA256)
	archiveFormat := req.ArchiveFormat
	if archiveFormat == "" {
		archiveFormat = "exe"
	}

	var pkg models.AIPackage
	err = tx.QueryRow(ctx, `
		INSERT INTO ai_packages (
			filename, sha256, size_bytes, version_label, notes,
			external_url, archive_format, entrypoint,
			uploaded_by, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (sha256) DO UPDATE SET
			version_label  = EXCLUDED.version_label,
			notes          = EXCLUDED.notes,
			external_url   = EXCLUDED.external_url,
			archive_format = EXCLUDED.archive_format,
			entrypoint     = EXCLUDED.entrypoint
		RETURNING id, filename, sha256, size_bytes, version_label, notes,
		          external_url, archive_format, entrypoint,
		          uploaded_by, uploaded_at, is_active, revoked_at
	`, req.Filename, sha, req.SizeBytes, req.VersionLabel, req.Notes,
		req.URL, archiveFormat, req.Entrypoint, uploadedBy, req.SetActive,
	).Scan(
		&pkg.ID, &pkg.Filename, &pkg.SHA256, &pkg.SizeBytes, &pkg.VersionLabel, &pkg.Notes,
		&pkg.ExternalURL, &pkg.ArchiveFormat, &pkg.Entrypoint,
		&pkg.UploadedBy, &pkg.UploadedAt, &pkg.IsActive, &pkg.RevokedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert external ai_package: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &pkg, nil
}

// GetActiveForAgent returns the metadata an enrolled agent needs to
// decide whether to update its local AI client copy. When the active
// package was registered with an external URL (CDN), agents fetch from
// that URL — much faster than the VPS for 35MB+ binaries.
func (s *AIPackageService) GetActiveForAgent(ctx context.Context) (*models.AgentAIPackageResponse, error) {
	var (
		sha           string
		size          int64
		versionLabel  string
		externalURL   *string
		archiveFormat string
		entrypoint    *string
	)
	err := s.db.Pool.QueryRow(ctx, `
		SELECT sha256, size_bytes, version_label, external_url,
		       archive_format, entrypoint
		FROM ai_packages
		WHERE is_active = TRUE AND revoked_at IS NULL
		LIMIT 1
	`).Scan(&sha, &size, &versionLabel, &externalURL, &archiveFormat, &entrypoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return &models.AgentAIPackageResponse{Available: false}, nil
	}
	if err != nil {
		return nil, err
	}

	downloadURL := s.publicDownloadURL
	if externalURL != nil && *externalURL != "" {
		downloadURL = *externalURL
	}

	resp := &models.AgentAIPackageResponse{
		Available:     true,
		SHA256:        sha,
		SizeBytes:     size,
		VersionLabel:  versionLabel,
		DownloadURL:   downloadURL,
		ArchiveFormat: archiveFormat,
	}
	if entrypoint != nil {
		resp.Entrypoint = *entrypoint
	}
	return resp, nil
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
