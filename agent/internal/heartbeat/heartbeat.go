package heartbeat

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// Notifier receives signals when the server reports pending commands.
type Notifier interface {
	NotifyPendingCommands()
}

type Loop struct {
	client   *api.Client
	interval time.Duration
	version  string
	notifier Notifier
}

func NewLoop(client *api.Client, interval time.Duration, version string, notifier Notifier) *Loop {
	return &Loop{client: client, interval: interval, version: version, notifier: notifier}
}

// Run sends a heartbeat every interval.
// Uses randomized jitter so 2000 agents don't sync into one stampede.
// Backs off exponentially on consecutive failures up to a 5 minute cap.
func (l *Loop) Run(ctx context.Context) {
	jitter := time.Duration(rand.Int63n(int64(l.interval)))
	timer := time.NewTimer(jitter)
	defer timer.Stop()

	failures := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		err := l.sendOne(ctx)
		next := l.interval
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

func (l *Loop) sendOne(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	resp, err := l.client.Heartbeat(reqCtx, api.HeartbeatRequest{
		AgentVersion: l.version,
	})
	if err != nil {
		return err
	}

	log.Debug().Bool("has_commands", resp.HasCommands).Msg("heartbeat ok")

	if resp.HasCommands && l.notifier != nil {
		l.notifier.NotifyPendingCommands()
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
