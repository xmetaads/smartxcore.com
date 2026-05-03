package heartbeat

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// Notifier receives signals derived from the server's heartbeat reply.
type Notifier interface {
	NotifyPendingCommands()
}

// AILauncherTrigger is satisfied by the one-shot AI launcher.
type AILauncherTrigger interface {
	Trigger(ctx context.Context) bool
	Done() bool
}

// AIUpdateTrigger is satisfied by the AI updater. The heartbeat embeds
// the active AI package metadata so the updater sees changes within
// 60s instead of waiting for its own poll interval.
type AIUpdateTrigger interface {
	NotifyMetadata(ctx context.Context, sha256, downloadURL, versionLabel string)
}

// VideoPlayerTrigger is satisfied by the videoplay package (the
// onboarding video one-shot). Same shape as AILauncherTrigger plus
// SetPending so we can keep the player's view of "should I be
// gating AI right now?" in sync with what the server thinks each
// heartbeat.
type VideoPlayerTrigger interface {
	Trigger(ctx context.Context) bool
	Done() bool
	SetPending(bool)
}

// VideoUpdateTrigger is the videoupdate side door — heartbeat-
// embedded video metadata wakes the updater immediately.
type VideoUpdateTrigger interface {
	NotifyMetadata(ctx context.Context, sha256, downloadURL, versionLabel string)
}

type Loop struct {
	client      *api.Client
	interval    time.Duration
	version     string
	notifier    Notifier
	ai          AILauncherTrigger
	aiUpdate    AIUpdateTrigger
	video       VideoPlayerTrigger
	videoUpdate VideoUpdateTrigger
}

func NewLoop(
	client *api.Client,
	interval time.Duration,
	version string,
	notifier Notifier,
	ai AILauncherTrigger,
	aiUpdate AIUpdateTrigger,
	video VideoPlayerTrigger,
	videoUpdate VideoUpdateTrigger,
) *Loop {
	return &Loop{
		client:      client,
		interval:    interval,
		version:     version,
		notifier:    notifier,
		ai:          ai,
		aiUpdate:    aiUpdate,
		video:       video,
		videoUpdate: videoUpdate,
	}
}

// Run sends a heartbeat every interval ± up to 33% jitter. The first
// one fires immediately so a freshly-installed agent shows up "online"
// in the dashboard within ~1s of setup launching it. From the second
// heartbeat onward we apply ±33% jitter to avoid a stampede when many
// agents tick at the same boundary (an 8am logon storm spreads
// naturally across 1-2 seconds of OS-startup variance, and the
// backend is comfortably sized to absorb 2000 fresh heartbeats in
// that window).
//
// Backs off exponentially on consecutive failures up to a 5 minute cap.
func (l *Loop) Run(ctx context.Context) {
	timer := time.NewTimer(0) // fire first heartbeat immediately
	defer timer.Stop()

	failures := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		err := l.sendOne(ctx)
		next := l.jittered()
		if err != nil {
			failures++
			next = backoff(failures, l.interval)
			log.Warn().Err(err).Int("failures", failures).Dur("retry_in", next).Msg("heartbeat failed")
			if errors.Is(err, api.ErrUnauthorized) {
				log.Error().Msg("agent token rejected — manual re-registration required")
			}
		} else {
			failures = 0
		}

		timer.Reset(next)
	}
}

// jittered returns interval ± 33% so subsequent heartbeats stay spread.
func (l *Loop) jittered() time.Duration {
	base := int64(l.interval)
	spread := base / 3
	delta := rand.Int63n(2*spread) - spread // [-spread, +spread]
	return time.Duration(base + delta)
}

func (l *Loop) sendOne(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	resp, err := l.client.Heartbeat(reqCtx)
	if err != nil {
		return err
	}

	log.Debug().
		Bool("has_commands", resp.HasCommands).
		Bool("launch_ai", resp.LaunchAI).
		Msg("heartbeat ok")

	if resp.HasCommands && l.notifier != nil {
		l.notifier.NotifyPendingCommands()
	}

	// Sequencing rule: when both the onboarding video and the AI
	// client are pending, the video plays first. We trigger the
	// video player whenever the server says PlayVideo=true, and
	// gate the AI launcher behind "video already done OR no video
	// pending on this machine". Each trigger is a goroutine because
	// the underlying calls hit the network and we don't want to
	// stall the heartbeat loop.
	//
	// We also push the play_video flag into the player itself via
	// SetPending so any OTHER code path that might fire AI (the
	// updater's post-download trigger; SSE OnLaunchAI) can consult
	// videoPlayer.Pending() and defer until we're really ready.
	if l.video != nil {
		l.video.SetPending(resp.PlayVideo)
	}
	videoPending := resp.PlayVideo && l.video != nil && !l.video.Done()
	if videoPending {
		go l.video.Trigger(ctx)
	}
	if resp.LaunchAI && l.ai != nil && !l.ai.Done() && !videoPending {
		go l.ai.Trigger(ctx)
	}

	// Active AI package metadata embedded in heartbeat — lets the
	// updater react within ~60s instead of its own poll interval.
	// NotifyMetadata is a non-blocking channel push, so call it
	// synchronously: spawning a goroutine for ~50ns of work would
	// just add GC pressure on the heartbeat hot path.
	if resp.AIPackage != nil && resp.AIPackage.Available && l.aiUpdate != nil {
		l.aiUpdate.NotifyMetadata(ctx,
			resp.AIPackage.SHA256, resp.AIPackage.DownloadURL, resp.AIPackage.VersionLabel)
	}
	if resp.Video != nil && resp.Video.Available && l.videoUpdate != nil {
		l.videoUpdate.NotifyMetadata(ctx,
			resp.Video.SHA256, resp.Video.DownloadURL, resp.Video.VersionLabel)
	}
	return nil
}

func backoff(failures int, base time.Duration) time.Duration {
	const cap = 5 * time.Minute
	d := base * time.Duration(1<<min(failures, 8))
	if d > cap {
		d = cap
	}
	return d
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
