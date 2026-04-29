package models

import (
	"time"

	"github.com/google/uuid"
)

// AIPackage represents an AI client binary registered with the system.
// Two storage modes:
//   - Local upload: bytes live on the VPS, served by nginx /downloads/.
//   - External URL: bytes live on a CDN (e.g. Bunny). The backend never
//     stores the file, only the metadata. Agents fetch from external_url
//     and verify against sha256.
type AIPackage struct {
	ID           uuid.UUID  `json:"id"`
	Filename     string     `json:"filename"`
	SHA256       string     `json:"sha256"`
	SizeBytes    int64      `json:"size_bytes"`
	VersionLabel string     `json:"version_label"`
	Notes        *string    `json:"notes,omitempty"`
	ExternalURL  *string    `json:"external_url,omitempty"`
	UploadedBy   uuid.UUID  `json:"uploaded_by"`
	UploadedAt   time.Time  `json:"uploaded_at"`
	IsActive     bool       `json:"is_active"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// RegisterExternalAIPackageRequest is the body for /admin/ai-packages/external.
// No file bytes — admin has already uploaded to their CDN and provides
// the URL plus integrity metadata.
type RegisterExternalAIPackageRequest struct {
	URL          string  `json:"url" validate:"required,url"`
	SHA256       string  `json:"sha256" validate:"required,len=64,hexadecimal"`
	SizeBytes    int64   `json:"size_bytes" validate:"required,min=1,max=2147483648"`
	VersionLabel string  `json:"version_label" validate:"required,min=1,max=64"`
	Filename     string  `json:"filename" validate:"required,min=1,max=200"`
	Notes        *string `json:"notes,omitempty" validate:"omitempty,max=500"`
	SetActive    bool    `json:"set_active"`
}

// AgentAIPackageResponse is what /api/v1/agent/ai-package returns to
// the agent: just the metadata it needs to decide whether to download
// a new version. The download URL is a stable path served by nginx so
// the agent does not need a signed URL.
type AgentAIPackageResponse struct {
	Available    bool   `json:"available"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	VersionLabel string `json:"version_label,omitempty"`
	DownloadURL  string `json:"download_url,omitempty"`
}
