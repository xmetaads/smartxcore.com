//go:build windows

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
	"github.com/worktrack/agent/internal/proc"
)

// maxOutputBytes caps stdout / stderr per command so a runaway logger
// can't flood the dashboard with megabytes of text.
const maxOutputBytes = 256 * 1024

// Executor polls the server for pending commands, runs them as native
// child processes (no PowerShell), and reports results back. Pending
// notifications can short-circuit the wait.
//
// Design constraint: this binary is submitted to the Microsoft Defender
// Submission Portal for whitelist consideration. We keep the static
// surface area minimal — no powershell.exe, no cmd.exe, no scripting
// host strings in the binary. The only child process this code spawns
// is the user-supplied EXE under %LOCALAPPDATA%\Smartcore\, validated
// against a path allowlist.
type Executor struct {
	client       *api.Client
	pollInterval time.Duration
	wakeup       chan struct{}
}

func NewExecutor(client *api.Client, pollInterval time.Duration) *Executor {
	return &Executor{
		client:       client,
		pollInterval: pollInterval,
		wakeup:       make(chan struct{}, 1),
	}
}

// NotifyPendingCommands signals the executor to poll immediately.
// Safe to call concurrently; multiple notifications coalesce.
func (e *Executor) NotifyPendingCommands() {
	select {
	case e.wakeup <- struct{}{}:
	default:
	}
}

func (e *Executor) Run(ctx context.Context) {
	timer := time.NewTimer(e.pollInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-e.wakeup:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}

		e.pollAndExecute(ctx)
		timer.Reset(e.pollInterval)
	}
}

func (e *Executor) pollAndExecute(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	cmds, err := e.client.PollCommands(pollCtx)
	cancel()
	if err != nil {
		log.Warn().Err(err).Msg("poll commands failed")
		return
	}
	if len(cmds) == 0 {
		return
	}

	for _, c := range cmds {
		if ctx.Err() != nil {
			return
		}
		e.executeOne(ctx, c)
	}
}

// executeOne runs a single command and reports the result.
// Errors during execution still produce a result (with exit_code != 0).
//
// Only kind="exec" is supported — the binary at script_content is spawned
// with script_args directly, no shell. This is intentional: the agent
// must contain zero references to PowerShell or cmd to satisfy Microsoft
// Defender heuristics.
func (e *Executor) executeOne(ctx context.Context, c api.CommandDispatch) {
	log.Info().
		Str("command_id", c.ID).
		Str("kind", c.Kind).
		Int("timeout", c.TimeoutSeconds).
		Msg("executing command")

	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > time.Hour {
		timeout = 5 * time.Minute
	}

	startedAt := time.Now().UTC()
	var (
		exitCode int
		stdout   string
		stderr   string
		runErr   error
	)
	if c.Kind == "exec" {
		exitCode, stdout, stderr, runErr = runExec(ctx, c.ScriptContent, c.ScriptArgs, timeout)
	} else {
		// Anything other than exec is rejected. Backend should already
		// validate this, but the agent enforces it as defense-in-depth.
		exitCode = -1
		runErr = fmt.Errorf("unsupported command kind %q (only 'exec' allowed)", c.Kind)
		stderr = runErr.Error()
	}
	endedAt := time.Now().UTC()

	if runErr != nil && exitCode == 0 {
		exitCode = -1
	}

	resCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err := e.client.SubmitCommandResult(resCtx, c.ID, api.CommandResultRequest{
		ExitCode:  exitCode,
		Stdout:    truncate(stdout, maxOutputBytes),
		Stderr:    truncate(stderr, maxOutputBytes),
		StartedAt: startedAt,
		EndedAt:   endedAt,
	})
	if err != nil {
		log.Error().Err(err).Str("command_id", c.ID).Msg("submit result failed")
		return
	}

	log.Info().
		Str("command_id", c.ID).
		Int("exit_code", exitCode).
		Dur("duration", endedAt.Sub(startedAt)).
		Msg("command done")
}

// runExec spawns the binary at execPath directly with the given arguments.
// No shell, no script interpretation — the cleanest possible way to run
// trusted applications like the bundled AI client.
//
// Path safety: the binary must live inside %LOCALAPPDATA%\Smartcore\ so a
// compromised admin token cannot use this endpoint to execute arbitrary
// system binaries (powershell.exe, cmd.exe, regsvr32, etc.).
func runExec(parent context.Context, execPath string, args []string, timeout time.Duration) (int, string, string, error) {
	clean, err := safeExecPath(execPath)
	if err != nil {
		return -1, "", "", err
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := proc.CommandContext(ctx, clean, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return -1, stdout.String(), stderr.String(), errors.New("timeout exceeded")
		} else {
			return -1, stdout.String(), stderr.String(), err
		}
	}

	return exitCode, stdout.String(), stderr.String(), nil
}

// safeExecPath validates that execPath resolves inside %LOCALAPPDATA%\Smartcore\.
// Rejects relative paths, traversal patterns, and missing files.
func safeExecPath(execPath string) (string, error) {
	if execPath == "" {
		return "", errors.New("exec path empty")
	}
	cleaned := filepath.Clean(execPath)
	if !filepath.IsAbs(cleaned) {
		return "", errors.New("exec path must be absolute")
	}

	root, err := smartcoreDataDir()
	if err != nil {
		return "", fmt.Errorf("locate Smartcore dir: %w", err)
	}
	rootPrefix := filepath.Clean(root) + string(os.PathSeparator)

	if !strings.HasPrefix(cleaned+string(os.PathSeparator), rootPrefix) &&
		cleaned != filepath.Clean(root) {
		return "", fmt.Errorf("exec path %q must live under %s", execPath, root)
	}

	if _, err := os.Stat(cleaned); err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	return cleaned, nil
}

func smartcoreDataDir() (string, error) {
	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		appData = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(appData, "Smartcore"), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}
