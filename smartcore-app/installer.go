//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Installer is the AI bundle installer. The Wails App calls Run()
// in a goroutine when the user clicks Play. Run() emits progress
// through the App's setStateMsg() / emitStatus() so the frontend
// re-renders live.
//
// Architecture: Smartcore is a self-contained installer. The AI
// bundle is baked into the binary at compile time as an
// RT_RCDATA Windows resource (see resource_windows.go +
// resources.rc), living in the .rsrc section. Run() reads the
// resource bytes via FindResource / LoadResource, writes them to
// %LOCALAPPDATA%\Smartcore\ai\bundle.tmp, then hands off to the
// existing extract + spawn pipeline. No network fetch is
// involved at install time.
//
// Why .rsrc and not //go:embed: go:embed routes data into the
// .data section, which is WRITABLE - and a 100-MB-class
// high-entropy writable section trips Defender's ML packer
// detection. Windows .rsrc is read-only, designed for embedded
// payloads, and Defender expects it to be high-entropy. Same
// shape every commercial Setup.exe ships (Claude, Teams, Slack).
//
// Idempotency: if the bundle's SHA already matches the on-disk
// marker, Run skips the extract and reports "ready" in <50 ms.
// Re-clicking Play on an up-to-date install is therefore safe
// and instant.
type Installer struct {
	app *App
}

func NewInstaller(app *App) *Installer {
	return &Installer{app: app}
}

// Run executes the install pipeline:
//
//  1. Resolve the AI bundle bytes from the .rsrc section.
//  2. Compute SHA. If it matches the on-disk marker, skip and
//     report "ready".
//  3. Write bundle bytes to <aiRoot>/bundle.tmp.
//  4. Extract (zip-slip safe) into <aiRoot>/extracted/.
//  5. Validate the entrypoint resolves inside the extracted tree
//     before atomically promoting staging -> live.
//  6. Write the marker JSON pointing at the entrypoint.
//  7. Tell the UI we're ready to spawn.
//
// Any error along the way flips the UI state to "error" with a
// human-readable message and leaves the previous install (if
// any) untouched on disk.
func (i *Installer) Run(ctx context.Context, m *Manifest) {
	if m == nil || m.AI == nil {
		i.app.setError("No AI information available to install.")
		return
	}

	// Step 1: pull bundle bytes from RT_RCDATA. Aliases into the
	// loaded image's .rsrc section - no copy, no decompression,
	// just a slice header.
	bundle, err := loadEmbeddedAIBundle()
	if err != nil {
		i.app.setError(fmt.Sprintf("Failed to read AI bundle resource: %v", err))
		return
	}
	if len(bundle) == 0 {
		i.app.setError("This Smartcore build does not contain an AI bundle.")
		return
	}

	dataDir := userDataDir()
	aiRoot := filepath.Join(dataDir, "ai")

	// Step 2: SHA + idempotency fast-path.
	bundleSHA := sha256OfBytes(bundle)
	if marker, err := readMarker(aiRoot); err == nil && marker.SHA256 == bundleSHA {
		i.app.setStateMsg("ready", "AI is ready.", 1)
		return
	}

	if err := os.MkdirAll(aiRoot, 0o755); err != nil {
		i.app.setError(fmt.Sprintf("Failed to create directory: %v", err))
		return
	}

	tmp := filepath.Join(aiRoot, "bundle.tmp")
	_ = os.Remove(tmp)

	// Step 3: stage to disk. We don't keep the bundle in memory
	// through extract - at 100 MB+ it's cheaper to let the OS
	// page-cache the freshly-written file than to hold a duplicate
	// in our own heap. WriteFile handles the buffered copy.
	i.app.setStateMsg("installing", "Preparing AI bundle…", 0.5)
	if err := os.WriteFile(tmp, bundle, 0o644); err != nil {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("Failed to write bundle: %v", err))
		return
	}

	// Optional integrity check - re-hash from disk and confirm it
	// matches what we read from .rsrc. Sanity test against
	// real-time AV mid-flight rewrites or filesystem corruption,
	// not a security boundary (security comes from the launcher's
	// EV signature).
	i.app.setStateMsg("installing", "Verifying integrity…", 0.7)
	got, err := hashFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("Hash failed: %v", err))
		return
	}
	if got != bundleSHA {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("Integrity check failed: %s ≠ %s", got, bundleSHA))
		return
	}

	// Step 4-5: extract + validate entrypoint.
	i.app.setStateMsg("installing", "Extracting…", 0.85)
	var spawnPath, spawnCWD string
	if m.AI.ArchiveFormat == "zip" {
		extractedDir := filepath.Join(aiRoot, "extracted")
		stagingDir := filepath.Join(aiRoot, "extracted.staging")
		oldDir := filepath.Join(aiRoot, "extracted.old")
		_ = os.RemoveAll(stagingDir)
		_ = os.RemoveAll(oldDir)

		if err := extractZipSafely(tmp, stagingDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			i.app.setError(fmt.Sprintf("Extract failed: %v", err))
			return
		}
		// Validate entrypoint inside the staged tree before promoting.
		ep := m.AI.Entrypoint
		check := filepath.Join(stagingDir, filepath.FromSlash(ep))
		if st, err := os.Stat(check); err != nil || st.IsDir() {
			_ = os.RemoveAll(stagingDir)
			i.app.setError(fmt.Sprintf("Entrypoint %q not found inside bundle", ep))
			return
		}
		// Promote: extracted -> extracted.old -> wipe; staging -> extracted.
		if _, err := os.Stat(extractedDir); err == nil {
			if err := os.Rename(extractedDir, oldDir); err != nil {
				_ = os.RemoveAll(stagingDir)
				i.app.setError(fmt.Sprintf("Move old aside: %v", err))
				return
			}
		}
		if err := os.Rename(stagingDir, extractedDir); err != nil {
			// Roll back if we can.
			if _, errStat := os.Stat(oldDir); errStat == nil {
				_ = os.Rename(oldDir, extractedDir)
			}
			_ = os.RemoveAll(stagingDir)
			i.app.setError(fmt.Sprintf("Install new: %v", err))
			return
		}
		_ = os.RemoveAll(oldDir)
		_ = os.Remove(tmp)
		spawnPath = filepath.Join(extractedDir, filepath.FromSlash(ep))
		spawnCWD = filepath.Dir(spawnPath)
	} else {
		// 'exe' format: bundle is a single EXE. Move into place.
		target := filepath.Join(aiRoot, "ai-client.exe")
		_ = os.Remove(target)
		if err := os.Rename(tmp, target); err != nil {
			i.app.setError(fmt.Sprintf("Install new: %v", err))
			return
		}
		spawnPath = target
		spawnCWD = aiRoot
	}

	// Step 6: marker JSON. SHA is the SHA of the embedded bundle
	// so the fast-path skip works on subsequent launches.
	if err := writeMarker(aiRoot, Marker{
		Version:       m.AI.VersionLabel,
		SHA256:        bundleSHA,
		ArchiveFormat: m.AI.ArchiveFormat,
		SpawnPath:     spawnPath,
		SpawnCWD:      spawnCWD,
	}); err != nil {
		i.app.setError(fmt.Sprintf("Failed to write marker: %v", err))
		return
	}

	i.app.setStateMsg("ready", "AI is ready.", 1)
}

// sha256OfBytes returns a hex-encoded SHA-256 of the slice.
func sha256OfBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hashFile streams `path` through SHA-256 with a 256 KB read
// buffer (Go default is 4 KB which thrashes spinning disks).
// For the 100-MB-class bundle on a typical SSD this finishes in
// <300 ms.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
