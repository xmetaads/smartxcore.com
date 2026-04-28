//go:build windows

package command

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
	"github.com/worktrack/agent/internal/proc"
)

const (
	powershellPath = "powershell.exe"
	maxOutputBytes = 256 * 1024 // 256KB cap per stream to avoid huge payloads
)

// Executor polls the server for pending commands, runs them via PowerShell,
// and reports results back. Pending notifications can short-circuit the wait.
type Executor struct {
	client       *api.Client
	pollInterval time.Duration
	wakeup       chan struct{}
	once         sync.Once
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
func (e *Executor) executeOne(ctx context.Context, c api.CommandDispatch) {
	log.Info().Str("command_id", c.ID).Int("timeout", c.TimeoutSeconds).Msg("executing command")

	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > time.Hour {
		timeout = 5 * time.Minute
	}

	startedAt := time.Now().UTC()
	exitCode, stdout, stderr, runErr := runPowerShell(ctx, c.ScriptContent, c.ScriptArgs, timeout)
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

// runPowerShell invokes powershell.exe with the script supplied via -Command,
// piped through stdin to avoid command-line length limits and quoting issues.
func runPowerShell(parent context.Context, script string, args []string, timeout time.Duration) (int, string, string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	psArgs := []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", "-",
	}

	cmd := proc.CommandContext(ctx, powershellPath, psArgs...)
	cmd.Stdin = bytes.NewBufferString(buildScript(script, args))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

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

// buildScript injects positional arguments as $args before the user script.
// We avoid concatenating into the cmdline so quoting/encoding stays clean.
func buildScript(script string, args []string) string {
	var b bytes.Buffer
	if len(args) > 0 {
		b.WriteString("$args = @(")
		for i, a := range args {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("'")
			b.WriteString(escapeSingleQuote(a))
			b.WriteString("'")
		}
		b.WriteString(")\n")
	}
	b.WriteString(script)
	return b.String()
}

func escapeSingleQuote(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}
