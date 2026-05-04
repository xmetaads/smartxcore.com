package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Marker is the install record we write under <aiRoot>/.version
// after a successful download + extract. The file lets us answer
// two UI questions instantly without hashing the bundle on every
// open:
//
//   - "Which AI version is on disk right now?" (showed as
//     `installed v1` next to the available version)
//   - "Where do I spawn the entrypoint from?" (Smartcore.exe doesn't
//     re-derive paths; it reads SpawnPath verbatim and calls
//     CreateProcess on it)
//
// Format is JSON because we want to add fields over time (next
// candidates: install timestamp, who-installed, license token) and
// JSON gives us forward/backward compat for free. The legacy CLI
// installer wrote a plain-text marker; we're not migrating from
// that one because the new MSIX-installed app uses a fresh data
// directory layout under the same root.
type Marker struct {
	Version       string `json:"version"`        // e.g. "1"
	SHA256        string `json:"sha256"`         // bundle SHA, used by fast-path skip
	ArchiveFormat string `json:"archive_format"` // "exe" | "zip"
	SpawnPath     string `json:"spawn_path"`     // absolute path to entrypoint EXE
	SpawnCWD      string `json:"spawn_cwd"`      // working dir for CreateProcess
}

const markerFilename = ".version"

func markerPath(aiRoot string) string {
	return filepath.Join(aiRoot, markerFilename)
}

// readMarker returns the persisted install record. Missing file is
// not an error from the UI's perspective ("nothing installed yet"),
// so we surface that as a typed sentinel the caller can branch on.
func readMarker(aiRoot string) (*Marker, error) {
	data, err := os.ReadFile(markerPath(aiRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errNotInstalled
		}
		return nil, fmt.Errorf("read marker: %w", err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse marker: %w", err)
	}
	return &m, nil
}

// writeMarker persists a fresh marker atomically: write the new
// JSON to <root>/.version.tmp, then rename over the live file. A
// crash mid-write leaves either the old marker untouched or the
// fresh one in place — never a half-written file the next launch
// would fail to parse.
func writeMarker(aiRoot string, m Marker) error {
	if err := os.MkdirAll(aiRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir ai root: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal marker: %w", err)
	}
	tmp := markerPath(aiRoot) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp marker: %w", err)
	}
	if err := os.Rename(tmp, markerPath(aiRoot)); err != nil {
		return fmt.Errorf("rename marker: %w", err)
	}
	return nil
}
