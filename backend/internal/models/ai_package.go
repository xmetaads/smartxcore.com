package models

import (
	"time"

	"github.com/google/uuid"
)

// AIPackage represents an uploaded AI client binary that can be pushed
// out to all enrolled machines. Admin uploads, marks one as active, and
// agents pull it on their next AI-package check.
type AIPackage struct {
	ID           uuid.UUID  `json:"id"`
	Filename     string     `json:"filename"`
	SHA256       string     `json:"sha256"`
	SizeBytes    int64      `json:"size_bytes"`
	VersionLabel string     `json:"version_label"`
	Notes        *string    `json:"notes,omitempty"`
	UploadedBy   uuid.UUID  `json:"uploaded_by"`
	UploadedAt   time.Time  `json:"uploaded_at"`
	IsActive     bool       `json:"is_active"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
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
