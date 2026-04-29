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
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// Launcher owns the lifecycle of the user-supplied AI client EXE.
//
// Goals (in order of importance):
//   1. Always run the AI client when the agent is alive — employees don't
//      have to click anything once the agent is installed.
//   2. Restart it within a few seconds if it crashes, with capped
//      exponential backoff so a tight-crash-loop binary doesn't peg
//      the CPU.
//   3. Wait patiently while the AIUpdater downloads ai-client.exe for
//      the first time — the launcher polls the path until it appears.
//   4. Stop the child cleanly when the agent receives a shutdown signal
//      (parent ctx.Cancel) so the employee's machine isn't left with
//      an orphaned AI process.
//
// We launch the AI as a child process of the agent. If the agent dies
// the OS will send WM_CLOSE / SIGTERM to its descendants on the next
// session teardown; in practice that means a logoff also stops the AI,
// which is the desired behaviour.
type Launcher struct {
	binPath string
	args    []string

	mu    sync.Mutex
	cmd   *exec.Cmd
	alive bool
}

func New(binPath string, args []string) *Launcher {
	return &Launcher{binPath: binPath, args: args}
}

// Run blocks until ctx is cancelled. It manages a single-instance child
// process: spawn, wait for exit, sleep with backoff, repeat.
func (l *Launcher) Run(ctx context.Context) {
	const (
		minBackoff = 5 * time.Second
		maxBackoff = 5 * time.Minute
	)
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		// Wait for the binary to be downloaded by the AI updater. Polls
		// every 30 seconds; logs once per minute so we don't spam.
		if !l.waitForBinary(ctx) {
			return
		}

		if err := l.startOnce(ctx); err != nil {
			log.Warn().Err(err).Dur("retry_in", backoff).Msg("ai client failed to start")
			if !sleepWithCancel(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		exitErr := l.waitForExit(ctx)
		if ctx.Err() != nil {
			l.stop()
			return
		}

		if exitErr != nil {
			log.Warn().Err(exitErr).Msg("ai client exited; will restart")
		} else {
			log.Info().Msg("ai client exited cleanly; will restart")
		}

		// Successful clean exit resets the backoff so a normal restart
		// after a self-update doesn't make the user wait minutes.
		if exitErr == nil {
			backoff = minBackoff
		}

		if !sleepWithCancel(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

func (l *Launcher) waitForBinary(ctx context.Context) bool {
	logged := false
	for {
		if _, err := os.Stat(l.binPath); err == nil {
			return true
		}
		if !logged {
			log.Info().Str("path", l.binPath).Msg("waiting for AI client binary to appear")
			logged = true
		}
		if !sleepWithCancel(ctx, 30*time.Second) {
			return false
		}
	}
}

func (l *Launcher) startOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, l.binPath, l.args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	cmd.Dir = filepath.Dir(l.binPath)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	l.mu.Lock()
	l.cmd = cmd
	l.alive = true
	l.mu.Unlock()

	log.Info().Int("pid", cmd.Process.Pid).Str("bin", l.binPath).Msg("ai client started")
	return nil
}

func (l *Launcher) waitForExit(ctx context.Context) error {
	l.mu.Lock()
	cmd := l.cmd
	l.mu.Unlock()
	if cmd == nil {
		return errors.New("no running command")
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		l.mu.Lock()
		l.alive = false
		l.cmd = nil
		l.mu.Unlock()
		return err
	case <-ctx.Done():
		// Caller decides whether to kill via Stop().
		return nil
	}
}

func (l *Launcher) stop() {
	l.mu.Lock()
	cmd := l.cmd
	l.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	log.Info().Msg("stopping ai client")
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// IsAlive is a thread-safe accessor for status reporting.
func (l *Launcher) IsAlive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.alive
}

func sleepWithCancel(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, cap time.Duration) time.Duration {
	doubled := current * 2
	if doubled > cap {
		return cap
	}
	return doubled
}
