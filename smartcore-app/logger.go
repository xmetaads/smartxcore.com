package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// initLogger wires zerolog to %LOCALAPPDATA%\Smartcore\logs\install.log.
// Called from App.startup() after the user-scope data directory has
// been resolved. The log is append-mode so successive launches build
// up history rather than wiping past sessions — useful for IT
// support diagnosing "what did Smartcore do last time".
//
// Best-effort: if anything in the logger setup fails (disk full,
// AppData read-only, antivirus quarantine on the log file) we fall
// back to stderr so the app keeps running. A missing log is annoying
// but not fatal; a Smartcore that refuses to start because logging
// failed would be worse.
func initLogger() {
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
		return
	}
	logDir := filepath.Join(appData, "Smartcore", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
		return
	}

	f, err := os.OpenFile(
		filepath.Join(logDir, "install.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
		return
	}
	log.Logger = zerolog.New(f).With().Timestamp().Logger()
}
