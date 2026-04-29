//go:build windows

package ailauncher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// Launcher is a one-shot AI client spawner. It does NOT loop or restart.
// The server tells the agent (via the heartbeat response) to launch the
// AI client; on success the agent acks and the server stops asking. The
// agent never relaunches the AI on its own — the user explicitly asked
// for "launch once, then leave the AI training session alone".
//
// Behaviour:
//   - Trigger() asks the launcher to attempt a spawn. Concurrent calls
//     coalesce: only one attempt is in flight at a time.
//   - If the binary doesn't exist yet (still being downloaded by the
//     updater), Trigger returns false and the next heartbeat will try
//     again.
//   - On a successful spawn, ackFn is called so the server can flip
//     ai_launched_at and stop sending launch=true on heartbeats.
//   - The spawned process is detached: if the agent exits, the AI
//     client survives. Agent does not Wait() on it.
type Launcher struct {
	binPath string
	args    []string
	ackFn   func(context.Context) error

	inFlight int32 // atomic: 0 or 1
	once     sync.Once
	done     atomic.Bool
}

func New(binPath string, args []string, ackFn func(context.Context) error) *Launcher {
	return &Launcher{binPath: binPath, args: args, ackFn: ackFn}
}

// Trigger attempts to launch the AI client. Returns true if the spawn
// succeeded (and the ack to the server succeeded). Returns false on
// transient failure — the caller should retry on the next heartbeat.
//
// Once Trigger returns true, all subsequent calls are no-ops: this
// launcher is intentionally single-fire per agent process lifetime.
func (l *Launcher) Trigger(ctx context.Context) bool {
	if l.done.Load() {
		return true
	}
	if !atomic.CompareAndSwapInt32(&l.inFlight, 0, 1) {
		return false // another Trigger is currently working
	}
	defer atomic.StoreInt32(&l.inFlight, 0)

	if err := l.attemptLaunch(ctx); err != nil {
		log.Warn().Err(err).Msg("ai client launch attempt failed; will retry on next heartbeat")
		return false
	}

	ackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := l.ackFn(ackCtx); err != nil {
		// Spawn worked but the server didn't get our ack. Don't mark
		// done — let the next heartbeat re-trigger so the agent posts
		// the ack again. Spawning twice would be wrong, so we early-
		// return done=true here only after a successful ack.
		log.Warn().Err(err).Msg("ai launched but ack failed; will re-ack")
		return false
	}

	l.done.Store(true)
	log.Info().Msg("ai client launched successfully (one-shot complete)")
	return true
}

func (l *Launcher) attemptLaunch(ctx context.Context) error {
	if _, err := os.Stat(l.binPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ai binary not present at %s — still downloading?", l.binPath)
		}
		return fmt.Errorf("stat ai binary: %w", err)
	}

	cmd := exec.CommandContext(ctx, l.binPath, l.args...)
	cmd.Dir = filepath.Dir(l.binPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// CREATE_NO_WINDOW (0x08000000): no console for child.
		// DETACHED_PROCESS (0x00000008): child has no parent console
		// and survives the agent's exit cleanly. The agent does NOT
		// Wait() on it — fire and forget.
		CreationFlags: 0x08000000 | 0x00000008,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	pid := -1
	if cmd.Process != nil {
		pid = cmd.Process.Pid
		// Release the handle so the OS can reap the process when it
		// exits — we don't track the lifecycle.
		_ = cmd.Process.Release()
	}
	log.Info().Int("pid", pid).Str("bin", l.binPath).Msg("ai client spawned (detached)")
	return nil
}

// Done reports whether this launcher has already completed its one-shot
// launch. Used for status reporting.
func (l *Launcher) Done() bool { return l.done.Load() }
