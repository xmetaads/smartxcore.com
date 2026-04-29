//go:build windows

package aiupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// Updater keeps the local AI client in sync with the active version
// published by the dashboard. On startup and then every interval it
//
//   1. Asks the server which SHA256 is active.
//   2. Hashes the local %LOCALAPPDATA%\Smartcore\ai\ai-client.exe.
//   3. If they differ (or the local file is missing), downloads the
//      published binary, verifies the SHA256 against the metadata, and
//      atomically swaps it into place.
//
// SHA256 verification is what makes this safe: even if the agent
// downloads from a public URL, a man-in-the-middle who substitutes
// bytes will fail the hash check and the swap is aborted.
type Updater struct {
	client   *api.Client
	dataDir  string
	interval time.Duration
}

func NewUpdater(client *api.Client, dataDir string, interval time.Duration) *Updater {
	return &Updater{client: client, dataDir: dataDir, interval: interval}
}

func (u *Updater) Run(ctx context.Context) {
	if err := u.tick(ctx); err != nil {
		log.Warn().Err(err).Msg("ai update tick failed")
	}

	timer := time.NewTimer(u.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := u.tick(ctx); err != nil {
			log.Warn().Err(err).Msg("ai update tick failed")
		}
		timer.Reset(u.interval)
	}
}

func (u *Updater) tick(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	meta, err := u.client.LatestAIPackage(reqCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("fetch metadata: %w", err)
	}
	if !meta.Available {
		log.Debug().Msg("no active ai package — skipping")
		return nil
	}

	target := u.localPath()
	localSHA, _ := hashFile(target)
	if localSHA == meta.SHA256 {
		log.Debug().Str("sha256", meta.SHA256[:12]).Msg("ai client up to date")
		return nil
	}

	log.Info().
		Str("from_sha", trim(localSHA)).
		Str("to_sha", trim(meta.SHA256)).
		Str("version", meta.VersionLabel).
		Msg("downloading new ai client")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	body, err := u.client.DownloadAIPackage(dlCtx, meta.DownloadURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer body.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hasher), body); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != meta.SHA256 {
		os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: want %s got %s", meta.SHA256, got)
	}

	// Atomic replace. On Windows you cannot rename over a file in use,
	// so if the AI client is currently running we move the old one out
	// of the way first.
	if _, err := os.Stat(target); err == nil {
		old := target + ".old"
		_ = os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("move old aside: %w", err)
		}
		_ = os.Remove(old)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("install new: %w", err)
	}

	if err := writeVersionMarker(filepath.Dir(target), meta); err != nil {
		log.Warn().Err(err).Msg("write version marker")
	}

	log.Info().Str("version", meta.VersionLabel).Msg("ai client updated")
	return nil
}

func (u *Updater) localPath() string {
	return filepath.Join(u.dataDir, "ai", "ai-client.exe")
}

// hashFile returns the SHA256 hex of the file at path. Missing files
// return an empty string with no error so the caller can treat them
// the same as "stale".
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
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeVersionMarker(aiDir string, meta *api.AIPackageResponse) error {
	path := filepath.Join(aiDir, ".version")
	body := fmt.Sprintf("%s\n%s\n", meta.VersionLabel, meta.SHA256)
	return os.WriteFile(path, []byte(body), 0o600)
}

func trim(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}
