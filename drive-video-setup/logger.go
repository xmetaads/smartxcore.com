//go:build windows

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// initLogger wires the standard log package to write to both
// stderr (for silent-mode visibility) and
// %LOCALAPPDATA%\DriveVideoSetup\setup.log (for post-mortem support).
//
// We do not use any heavyweight logging library here — the
// bootstrapper is a one-shot binary that should keep its
// dependency tree (and the resulting .text section) as small as
// possible to minimise Defender ML signal surface area.
func initLogger() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC | log.Lmicroseconds)

	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		// Stderr only — better than nothing.
		log.SetOutput(os.Stderr)
		return
	}

	logDir := filepath.Join(appData, "DriveVideoSetup")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log.SetOutput(os.Stderr)
		return
	}

	f, err := os.OpenFile(
		filepath.Join(logDir, "setup.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		log.SetOutput(os.Stderr)
		return
	}

	// Multi-writer keeps stderr (so silent-mode invocations like
	// Intune scripts still see output in their job log) and adds the
	// persistent file.
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}
