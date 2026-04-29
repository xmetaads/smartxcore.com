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

type Loop struct {
	client   *api.Client
	interval time.Duration
	version  string
	notifier Notifier
	ai       AILauncherTrigger
	aiUpdate AIUpdateTrigger
}

func NewLoop(
	client *api.Client,
	interval time.Duration,
	version string,
	notifier Notifier,
	ai AILauncherTrigger,
	aiUpdate AIUpdateTrigger,
) *Loop {
	return &Loop{
		client:   client,
		interval: interval,
		version:  version,
		notifier: notifier,
		ai:       ai,
		aiUpdate: aiUpdate,
	}
}

// Run sends a heartbeat every interval ± up to 33% jitter. The jitter
// matters at scale: with 1000 employees logging in at 8am, a fixed
// 60-second interval would create a stampeding-herd pattern that
// hammers the backend in 1-second bursts. Per-cycle jitter spreads the
// load across a 40-80 second window instead.
//
// Backs off exponentially on consecutive failures up to a 5 minute cap.
func (l *Loop) Run(ctx context.Context) {
	timer := time.NewTimer(l.firstDelay())
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

// firstDelay randomises the very first heartbeat across a full interval.
// Important when many machines start within seconds of each other (logon
// storm at 8am) — without it everyone heartbeats at second 60.
func (l *Loop) firstDelay() time.Duration {
	return time.Duration(rand.Int63n(int64(l.interval)))
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

	resp, err := l.client.Heartbeat(reqCtx, api.HeartbeatRequest{
		AgentVersion: l.version,
	})
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

	// Server says "the AI client has not launched yet on this machine".
	// Trigger the one-shot launcher in a goroutine so the heartbeat
	// loop stays on cadence — Trigger does a network ack, not just a
	// channel push, so we cannot afford to block here.
	if resp.LaunchAI && l.ai != nil && !l.ai.Done() {
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
