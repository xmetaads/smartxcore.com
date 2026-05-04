//go:build windows

// Package videoupdate downloads + verifies the active onboarding
// video described by the install config. One-shot mirror of the
// aiupdate.Installer with a simpler transport (single-stream HTTP
// GET — the video is small enough that chunked Range gives no
// real throughput win).
package videoupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// Installer is the one-shot video downloader. dataDir is the
// per-user Smartcore directory; the video lands at
// <dataDir>/video/video.mp4 and the SHA256 is recorded in
// <dataDir>/video/.version for idempotent re-runs.
type Installer struct {
	client  *api.Client
	dataDir string
}

func NewInstaller(client *api.Client, dataDir string) *Installer {
	return &Installer{client: client, dataDir: dataDir}
}

// InstallOnce ensures <dataDir>/video/video.mp4 matches meta.SHA256.
// Idempotent: a re-run on a machine whose marker already records
// the same SHA returns immediately without re-downloading.
func (u *Installer) InstallOnce(ctx context.Context, meta *api.VideoInfo) error {
	if meta == nil {
		return errors.New("nil meta")
	}
	if meta.URL == "" || meta.SHA256 == "" {
		return errors.New("meta missing url or sha256")
	}

	videoDir := filepath.Join(u.dataDir, "video")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		return fmt.Errorf("mkdir video dir: %w", err)
	}
	target := filepath.Join(videoDir, "video.mp4")
	markerPath := filepath.Join(videoDir, ".version")

	// Marker fast-path: already on disk with matching SHA.
	if cur, _ := os.ReadFile(markerPath); len(cur) > 0 && readMarkerSHA(string(cur)) == meta.SHA256 {
		log.Info().Str("sha256", short(meta.SHA256)).Msg("video already installed (marker hit)")
		return nil
	}

	log.Info().
		Str("to_sha", short(meta.SHA256)).
		Str("version", meta.VersionLabel).
		Int64("bytes", meta.SizeBytes).
		Msg("downloading onboarding video")

	tmp := target + ".new"
	_ = os.Remove(tmp)

	if err := download(ctx, u.client, meta.URL, tmp); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	got, err := hashFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hash tmp: %w", err)
	}
	if got != meta.SHA256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: want %s got %s", meta.SHA256, got)
	}

	// Atomic replace: rename old → tmp deletion, new → target.
	_ = os.Remove(target)
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("install new: %w", err)
	}

	// Marker is just "<version>\n<sha256>\n" — same shape as the
	// legacy aiupdate plain-text format, plenty for our needs.
	_ = os.WriteFile(markerPath, []byte(meta.VersionLabel+"\n"+meta.SHA256+"\n"), 0o644)

	log.Info().Str("path", target).Str("sha256", short(meta.SHA256)).Msg("video installed")
	return nil
}

func download(ctx context.Context, client *api.Client, url, tmp string) error {
	body, _, err := client.Download(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	bufOut := bufio.NewWriterSize(out, 256*1024)
	if _, err := io.Copy(bufOut, body); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := bufOut.Flush(); err != nil {
		_ = out.Close()
		return fmt.Errorf("flush: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	return out.Close()
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
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

// readMarkerSHA parses the second line of the legacy plain-text
// marker format: "<version>\n<sha256>\n". Returns empty string
// when the format doesn't match — the caller treats that as "no
// marker, must download".
func readMarkerSHA(s string) string {
	for i, line := 0, ""; i < len(s); {
		j := i
		for j < len(s) && s[j] != '\n' {
			j++
		}
		line = s[i:j]
		if i > 0 {
			return line // second line = sha256
		}
		i = j + 1
	}
	return ""
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
