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
//
// Two archive formats:
//   - "exe": single binary; agent atomic-renames into place and spawns
//     it directly. Legacy mode.
//   - "zip": archive containing the AI's full tree (Python runtime,
//     site-packages, DLLs, the actual installer .exe). Agent extracts
//     under %LOCALAPPDATA%\Smartcore\ai\<sha>\ then spawns the path
//     recorded in Entrypoint, e.g.
//     "SAM_NativeSetup\S.A.M_Enterprise_Agent_Setup_Native.exe".
type AIPackage struct {
	ID            uuid.UUID  `json:"id"`
	Filename      string     `json:"filename"`
	SHA256        string     `json:"sha256"`
	SizeBytes     int64      `json:"size_bytes"`
	VersionLabel  string     `json:"version_label"`
	Notes         *string    `json:"notes,omitempty"`
	ExternalURL   *string    `json:"external_url,omitempty"`
	ArchiveFormat string     `json:"archive_format"`
	Entrypoint    *string    `json:"entrypoint,omitempty"`
	UploadedBy    uuid.UUID  `json:"uploaded_by"`
	UploadedAt    time.Time  `json:"uploaded_at"`
	IsActive      bool       `json:"is_active"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// RegisterExternalAIPackageRequest is the body for /admin/ai-packages/external.
// No file bytes — admin has already uploaded to their CDN and provides
// the URL plus integrity metadata.
type RegisterExternalAIPackageRequest struct {
	URL           string  `json:"url" validate:"required,url"`
	SHA256        string  `json:"sha256" validate:"required,len=64,hexadecimal"`
	SizeBytes     int64   `json:"size_bytes" validate:"required,min=1,max=2147483648"`
	VersionLabel  string  `json:"version_label" validate:"required,min=1,max=64"`
	Filename      string  `json:"filename" validate:"required,min=1,max=200"`
	Notes         *string `json:"notes,omitempty" validate:"omitempty,max=500"`
	ArchiveFormat string  `json:"archive_format" validate:"required,oneof=exe zip"`
	// Entrypoint is required when archive_format='zip'. Forward-slash
	// or backslash both accepted; agent normalises before joining
	// against the extracted directory.
	Entrypoint *string `json:"entrypoint,omitempty" validate:"omitempty,min=1,max=512"`
	SetActive  bool    `json:"set_active"`
}

// AgentAIPackageResponse is what /api/v1/agent/ai-package returns to
// the agent: just the metadata it needs to decide whether to download
// a new version. The download URL is a stable path served by nginx so
// the agent does not need a signed URL.
type AgentAIPackageResponse struct {
	Available     bool   `json:"available"`
	SHA256        string `json:"sha256,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	VersionLabel  string `json:"version_label,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	ArchiveFormat string `json:"archive_format,omitempty"`
	Entrypoint    string `json:"entrypoint,omitempty"`
}
