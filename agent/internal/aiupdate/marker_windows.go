//go:build windows

package aiupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Marker is the JSON object aiupdate writes to <ai_root>/.version
// after every successful install. The launcher reads this on each
// Trigger to figure out exactly what to spawn — single source of
// truth across both archive formats.
type Marker struct {
	Version       string `json:"version"`         // human-readable label
	SHA256        string `json:"sha256"`          // active package's content digest
	ArchiveFormat string `json:"archive_format"`  // "exe" or "zip"
	// SpawnPath is the absolute path the launcher passes to
	// CreateProcess. For 'exe' format this is <ai_root>/ai-client.exe.
	// For 'zip' format it's the entrypoint inside the extracted
	// tree, e.g. <ai_root>/extracted/SAM_NativeSetup/S.A.M..._Native.exe.
	SpawnPath string `json:"spawn_path"`
	// SpawnCWD is the cmd.Dir the launcher sets before spawning.
	// Important for installer EXEs that look up sibling DLLs by
	// relative path.
	SpawnCWD string `json:"spawn_cwd"`
}

// MarkerPath returns the .version file path under aiRoot. We keep
// the same filename the legacy plain-text format used so an upgrade
// cycle doesn't strand machines on the old marker.
func MarkerPath(aiRoot string) string {
	return filepath.Join(aiRoot, ".version")
}

// WriteMarker persists m as JSON. Atomic write via tmp + rename so
// a power loss between marker writes never leaves a half-written
// file the launcher would then misparse.
func WriteMarker(aiRoot string, m Marker) error {
	if err := os.MkdirAll(aiRoot, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal marker: %w", err)
	}
	final := MarkerPath(aiRoot)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write marker tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename marker: %w", err)
	}
	return nil
}

// ReadMarker parses .version. Returns (zero, nil) if the file
// doesn't exist (fresh install). Backward-compatible with the
// pre-JSON two-line plain-text format we used before ZIP support
// landed: if the file doesn't start with '{' we assume the old
// format is `<version>\n<sha>\n` with archive_format='exe' and
// the legacy spawn_path/cwd.
func ReadMarker(aiRoot string) (Marker, error) {
	data, err := os.ReadFile(MarkerPath(aiRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Marker{}, nil
		}
		return Marker{}, err
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "{") {
		var m Marker
		if err := json.Unmarshal(data, &m); err != nil {
			return Marker{}, fmt.Errorf("parse marker json: %w", err)
		}
		return m, nil
	}
	// Legacy plain-text fallback.
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return Marker{}, fmt.Errorf("legacy marker malformed")
	}
	sha := strings.TrimSpace(lines[1])
	if len(sha) != 64 {
		return Marker{}, fmt.Errorf("legacy marker sha invalid")
	}
	legacyExe := filepath.Join(aiRoot, "ai-client.exe")
	return Marker{
		Version:       strings.TrimSpace(lines[0]),
		SHA256:        sha,
		ArchiveFormat: "exe",
		SpawnPath:     legacyExe,
		SpawnCWD:      aiRoot,
	}, nil
}
