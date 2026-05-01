package models

import (
	"time"

	"github.com/google/uuid"
)

// Video is one onboarding video registered for the fleet. Bytes live
// on a CDN (Bunny / R2 / wherever the admin uploaded them); we only
// store metadata + the public URL.
type Video struct {
	ID           uuid.UUID  `json:"id"`
	Filename     string     `json:"filename"`
	SHA256       string     `json:"sha256"`
	SizeBytes    int64      `json:"size_bytes"`
	VersionLabel string     `json:"version_label"`
	Notes        *string    `json:"notes,omitempty"`
	ExternalURL  string     `json:"external_url"`
	UploadedBy   uuid.UUID  `json:"uploaded_by"`
	UploadedAt   time.Time  `json:"uploaded_at"`
	IsActive     bool       `json:"is_active"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// RegisterExternalVideoRequest is the dashboard form payload that
// admins submit after uploading the video to a CDN. Same shape as
// the AI-package external-URL request — we keep videos and AI
// packages on the same UX so the admin only learns one workflow.
type RegisterExternalVideoRequest struct {
	URL          string  `json:"url" validate:"required,url"`
	SHA256       string  `json:"sha256" validate:"required,len=64,hexadecimal"`
	SizeBytes    int64   `json:"size_bytes" validate:"required,gt=0"`
	VersionLabel string  `json:"version_label" validate:"required,min=1,max=64"`
	Filename     string  `json:"filename" validate:"required,min=1,max=255"`
	Notes        *string `json:"notes,omitempty" validate:"omitempty,max=500"`
	SetActive    bool    `json:"set_active"`
}

// AgentVideoResponse is what /api/v1/agent/video returns and what the
// heartbeat embeds. Available=false means "no active video, don't
// play anything".
type AgentVideoResponse struct {
	Available    bool   `json:"available"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	VersionLabel string `json:"version_label,omitempty"`
	DownloadURL  string `json:"download_url,omitempty"`
}
