package handlers

import (
	"path/filepath"

	"github.com/gofiber/fiber/v2"
)

// AgentBinaryHandler serves the Smartcore.exe agent binary back to a
// caller that has just successfully enrolled (auth via X-Agent-Token,
// enforced by the AgentAuth middleware upstream).
//
// Why this exists: the original setup.exe embedded Smartcore.exe in
// its go:embed payload, which made the installer 13 MB and matched
// the "EXE-inside-EXE / dropper" pattern static-ML antivirus models
// flag aggressively (Symantec ML.Attribute.HighConfidence,
// SentinelOne Static AI Suspicious PE, CrowdStrike
// malicious_confidence_60%, Bkav AIDetectMalware, etc.). Splitting
// the agent payload off the installer and behind a real auth check:
//
//  1. Drops setup.exe size from 13 MB to ~4 MB (less malware-like).
//  2. Removes the "executable nested inside another executable"
//     signal entirely.
//  3. Ensures only freshly-enrolled clients can download the agent
//     binary — a malware sandbox running setup.exe still has to
//     successfully enroll first, and the deployment token can be
//     IP-restricted / require_email-restricted to prevent that.
//
// Layout: the binary lives at <storage>/Smartcore.exe on disk; we
// fetch it via Fiber's SendFile which streams with sendfile(2) on
// Linux for zero-copy delivery.
type AgentBinaryHandler struct {
	storageDir string
}

func NewAgentBinaryHandler(storageDir string) *AgentBinaryHandler {
	return &AgentBinaryHandler{storageDir: storageDir}
}

// Serve streams the active Smartcore.exe to an authenticated agent.
// Returns 404 if the binary isn't on disk yet (fresh install before
// the operator has uploaded one) so the caller knows to retry later
// or fall back to a manual recovery path.
func (h *AgentBinaryHandler) Serve(c *fiber.Ctx) error {
	path := filepath.Join(h.storageDir, "Smartcore.exe")
	c.Set(fiber.HeaderContentType, "application/octet-stream")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="Smartcore.exe"`)
	return c.SendFile(path)
}
