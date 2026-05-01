//go:build windows

// Package videoupdate keeps the local onboarding video in sync with
// whatever the dashboard has marked active. Mirror of the aiupdate
// package — same wakeup-channel + mutex single-flight + atomic
// rename pattern, simpler transport (single-stream HTTP GET because
// the video is small enough that chunked range adds no real
// throughput).
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
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// PlayerTrigger is what the updater calls after a successful
// download to nudge the videoplay package into showing the new
// video. Same one-way interface aiupdate uses for ailauncher.
type PlayerTrigger interface {
	Trigger(ctx context.Context) bool
	Done() bool
}

type Updater struct {
	client   *api.Client
	dataDir  string
	interval time.Duration

	mu      sync.Mutex
	wakeup  chan struct{}
	player  PlayerTrigger
}

func NewUpdater(client *api.Client, dataDir string, interval time.Duration, player PlayerTrigger) *Updater {
	return &Updater{
		client:   client,
		dataDir:  dataDir,
		interval: interval,
		wakeup:   make(chan struct{}, 1),
		player:   player,
	}
}

// NotifyMetadata is the heartbeat / SSE side door — it just kicks
// the wakeup channel so the next tick sees fresh metadata. Empty
// SHA means "no active video", we no-op.
func (u *Updater) NotifyMetadata(ctx context.Context, sha256, downloadURL, versionLabel string) {
	if sha256 == "" {
		return
	}
	select {
	case u.wakeup <- struct{}{}:
	default:
	}
}

func (u *Updater) Run(ctx context.Context) {
	if err := u.tick(ctx); err != nil {
		log.Warn().Err(err).Msg("video update tick failed")
	}
	timer := time.NewTimer(u.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-u.wakeup:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if err := u.tick(ctx); err != nil {
			log.Warn().Err(err).Msg("video update tick failed")
		}
		timer.Reset(u.interval)
	}
}

func (u *Updater) tick(ctx context.Context) error {
	if !u.mu.TryLock() {
		return nil
	}
	defer u.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	meta, err := u.client.LatestVideo(reqCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("fetch video metadata: %w", err)
	}
	if !meta.Available {
		log.Debug().Msg("no active video — skipping")
		return nil
	}

	target := u.localPath()
	if cached := readVersionMarkerSHA(filepath.Dir(target)); cached != "" && cached == meta.SHA256 {
		log.Debug().Str("sha256", trim(meta.SHA256)).Msg("video up to date (marker cache)")
		u.maybeTriggerPlayer(ctx)
		return nil
	}

	localSHA, _ := hashFile(target)
	if localSHA == meta.SHA256 {
		_ = writeVersionMarker(filepath.Dir(target), meta)
		u.maybeTriggerPlayer(ctx)
		return nil
	}

	log.Info().
		Str("from_sha", trim(localSHA)).
		Str("to_sha", trim(meta.SHA256)).
		Str("version", meta.VersionLabel).
		Int64("bytes", meta.SizeBytes).
		Msg("downloading new onboarding video")

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".new"

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	body, contentLen, err := u.client.DownloadAIPackage(dlCtx, meta.DownloadURL)
	if err != nil {
		return fmt.Errorf("video download: %w", err)
	}
	defer body.Close()
	if meta.SizeBytes > 0 && contentLen > 0 && contentLen != meta.SizeBytes {
		return fmt.Errorf("video size mismatch: metadata says %d, server sent %d", meta.SizeBytes, contentLen)
	}

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	bufOut := bufio.NewWriterSize(out, 256*1024)
	hasher := sha256.New()
	start := time.Now()
	written, err := io.Copy(io.MultiWriter(bufOut, hasher), body)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := bufOut.Flush(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != meta.SHA256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("video sha256 mismatch: want %s got %s", meta.SHA256, got)
	}

	if _, err := os.Stat(target); err == nil {
		old := target + ".old"
		_ = os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("move old video: %w", err)
		}
		_ = os.Remove(old)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("install video: %w", err)
	}

	if err := writeVersionMarker(filepath.Dir(target), meta); err != nil {
		log.Warn().Err(err).Msg("write video version marker")
	}

	dur := time.Since(start)
	mbps := 0.0
	if dur > 0 {
		mbps = float64(written) / dur.Seconds() / (1024 * 1024)
	}
	log.Info().
		Str("version", meta.VersionLabel).
		Int64("bytes", written).
		Dur("took", dur).
		Float64("mbps", mbps).
		Msg("onboarding video updated")

	u.maybeTriggerPlayer(ctx)
	return nil
}

func (u *Updater) maybeTriggerPlayer(ctx context.Context) {
	if u.player == nil || u.player.Done() {
		return
	}
	go u.player.Trigger(ctx)
}

func (u *Updater) localPath() string {
	return filepath.Join(u.dataDir, "video", "video.mp4")
}

// === helpers identical in spirit to aiupdate's ===

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

func writeVersionMarker(dir string, meta *api.VideoResponse) error {
	path := filepath.Join(dir, ".version")
	body := fmt.Sprintf("%s\n%s\n", meta.VersionLabel, meta.SHA256)
	return os.WriteFile(path, []byte(body), 0o600)
}

func readVersionMarkerSHA(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".version"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return ""
	}
	sha := strings.TrimSpace(lines[1])
	if len(sha) != 64 {
		return ""
	}
	return sha
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
