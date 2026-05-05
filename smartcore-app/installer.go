//go:build windows

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Installer is the AI bundle installer. The Wails App calls Run()
// in a goroutine when the user clicks "Cài đặt AI". Run() emits
// progress through the App's setStateMsg() / emitStatus() so the
// frontend redraws live.
//
// Idempotency: if the bundle's SHA already matches the on-disk
// marker, Run skips the download/extract and reports "ready" in
// under 50 ms. Re-clicking the install button on an up-to-date
// install is therefore safe and instant.
type Installer struct {
	app *App
}

func NewInstaller(app *App) *Installer {
	return &Installer{app: app}
}

// Run executes the install pipeline:
//
//   1. Compare manifest SHA to on-disk marker SHA. Match = skip.
//   2. Download the bundle to <aiRoot>/bundle.tmp with progress.
//   3. Verify SHA256 of the downloaded bytes.
//   4. Extract (zip-slip safe) into <aiRoot>/extracted/.
//   5. Write the marker JSON pointing at the entrypoint.
//   6. Tell the UI we're ready to spawn.
//
// Any error along the way flips the UI state to "error" with a
// human-readable message and leaves the previous install (if any)
// untouched on disk.
func (i *Installer) Run(ctx context.Context, m *Manifest) {
	if m == nil || m.AI == nil {
		i.app.setError("No AI information available to install.")
		return
	}

	dataDir := userDataDir()
	aiRoot := filepath.Join(dataDir, "ai")

	if marker, err := readMarker(aiRoot); err == nil && marker.SHA256 == m.AI.SHA256 {
		// Already installed at this exact SHA. Done.
		i.app.setStateMsg("ready", "AI is ready.", 1)
		return
	}

	if err := os.MkdirAll(aiRoot, 0o755); err != nil {
		i.app.setError(fmt.Sprintf("Failed to create directory: %v", err))
		return
	}

	tmp := filepath.Join(aiRoot, "bundle.tmp")
	_ = os.Remove(tmp)

	dlCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	i.app.setStateMsg("downloading", "Downloading AI bundle…", 0)
	written, err := i.download(dlCtx, m.AI.URL, m.AI.SizeBytes, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("Download failed: %v", err))
		return
	}
	if m.AI.SizeBytes > 0 && written != m.AI.SizeBytes {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("Size mismatch: got %d, expected %d", written, m.AI.SizeBytes))
		return
	}

	i.app.setStateMsg("installing", "Verifying SHA256…", 0.85)
	got, err := hashFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("Hash failed: %v", err))
		return
	}
	if got != m.AI.SHA256 {
		_ = os.Remove(tmp)
		i.app.setError(fmt.Sprintf("SHA256 mismatch: %s ≠ %s", got, m.AI.SHA256))
		return
	}

	i.app.setStateMsg("installing", "Extracting…", 0.9)
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
		// Promote: extracted → extracted.old → wipe; staging → extracted.
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

	if err := writeMarker(aiRoot, Marker{
		Version:       m.AI.VersionLabel,
		SHA256:        m.AI.SHA256,
		ArchiveFormat: m.AI.ArchiveFormat,
		SpawnPath:     spawnPath,
		SpawnCWD:      spawnCWD,
	}); err != nil {
		i.app.setError(fmt.Sprintf("Failed to write marker: %v", err))
		return
	}

	i.app.setStateMsg("ready", "AI is ready. Click \"Launch AI\" to run.", 1)
}

// download streams the URL to tmp with progress reporting through
// the App. Single connection, no chunked Range — keeps the surface
// small (Bunny CDN is fast enough on a single stream that the
// added complexity isn't worth it for an interactive UI).
func (i *Installer) download(ctx context.Context, url string, expectedSize int64, tmp string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept-Encoding", "identity") // never gzip a binary

	cli := &http.Client{Timeout: 0} // ctx is the deadline
	resp, err := cli.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	total := expectedSize
	if total <= 0 && resp.ContentLength > 0 {
		total = resp.ContentLength
	}

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("create tmp: %w", err)
	}
	bufOut := bufio.NewWriterSize(out, 256*1024)

	pw := &progressWriter{app: i.app, total: total}
	mw := io.MultiWriter(bufOut, pw)
	written, err := io.Copy(mw, resp.Body)
	if err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("copy: %w", err)
	}
	if err := bufOut.Flush(); err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("flush: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("fsync: %w", err)
	}
	return written, out.Close()
}

// progressWriter reports download progress to the UI by writing
// state every ~250 ms (more often is wasteful — the renderer can't
// repaint that fast anyway).
type progressWriter struct {
	app   *App
	total int64
	read  int64
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.read += int64(n)
	now := time.Now()
	if now.Sub(p.last) >= 250*time.Millisecond {
		p.last = now
		var pct float64
		if p.total > 0 {
			pct = float64(p.read) / float64(p.total)
			if pct > 0.85 {
				pct = 0.85 // last 15% is hash + extract
			}
		}
		mb := float64(p.read) / (1024 * 1024)
		totalMB := float64(p.total) / (1024 * 1024)
		msg := fmt.Sprintf("Downloading… %.1f / %.1f MB", mb, totalMB)
		p.app.setStateMsg("downloading", msg, pct)
	}
	return n, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	br := bufio.NewReaderSize(f, 256*1024)
	if _, err := io.Copy(h, br); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
