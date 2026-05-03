//go:build windows

// Package svcmain wraps the agent's run loop so it can be invoked by
// the Windows Service Control Manager (SCM). When `Smartcore.exe`
// is launched by SCM with the "service" sub-command, main() calls
// Run(runner) and this package wires up the SCM lifecycle:
//
//   1. Tell SCM "starting"
//   2. Spawn the runner's Run(ctx) in a goroutine
//   3. Tell SCM "running, accepts stop+shutdown"
//   4. Block on SCM control channel
//   5. On Stop/Shutdown: cancel ctx, wait for runner to exit
//   6. Tell SCM "stopped"
//
// Why a service instead of HKCU Run? Three reasons:
//
//   1. Defender ML cluster: HKCU\...\Run write is heuristic-flagged
//      as "trojan persistence". CreateServiceW + AutoStart is the
//      Microsoft-blessed path every legitimate enterprise app uses
//      (Tailscale, Datadog, CrowdStrike, NinjaOne).
//   2. Survives user logoff: agent keeps running when user logs out,
//      so heartbeat continues during shift changes / overnight.
//   3. LocalSystem account: agent has the rights it needs to install
//      future updates without prompting the user again.
//
// Service runs as LocalSystem; heartbeat traffic still goes out as
// the machine identity. No per-user state is collected (zero PII —
// see selfinstall package for the enrollment-time decision).
package svcmain

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows/svc"
)

// ServiceName is the name registered with SCM. Must match what
// selfinstall.Install creates and what `Smartcore.exe uninstall`
// looks up.
const ServiceName = "Smartcore"

// Runner is what the agent's main loop implements. Run blocks until
// either ctx is cancelled (graceful shutdown) or a fatal error
// occurs. Returning nil = clean stop; non-nil = service exit code 1.
type Runner interface {
	Run(ctx context.Context) error
}

// IsService returns true when the current process was started by
// SCM rather than by a user double-click. Used by main() to decide
// between "interactive install" and "real service mode".
func IsService() bool {
	is, err := svc.IsWindowsService()
	if err != nil {
		// Conservative: assume interactive on probe failure so we
		// don't crash a user-launched binary.
		return false
	}
	return is
}

// Run hands control to SCM. Blocks until SCM tells us to stop.
// Caller is the agent's main(); it should treat any error here as
// fatal.
func Run(runner Runner) error {
	if err := svc.Run(ServiceName, &handler{runner: runner}); err != nil {
		return fmt.Errorf("svc.Run: %w", err)
	}
	return nil
}

// stopGracePeriod is how long we wait for runner.Run to return after
// cancelling its context. Heartbeats and command results have at most
// 30s timeouts each, so 20s + a small buffer is enough for in-flight
// I/O to finish or abort.
const stopGracePeriod = 20 * time.Second

type handler struct {
	runner Runner
}

// Execute is the SCM callback. Pattern follows the example in
// golang.org/x/sys/windows/svc — see also Tailscale's
// cmd/tailscaled/service_windows.go for a production reference.
func (h *handler) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- h.runner.Run(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}
	log.Info().Msg("service running")

loop:
	for {
		select {
		case r := <-req:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Info().Uint32("cmd", uint32(r.Cmd)).Msg("service stop requested")
				status <- svc.Status{State: svc.StopPending}
				cancel()
				break loop
			default:
				// Ignore unexpected control codes (Pause/Continue we
				// don't accept; SCM shouldn't send them).
			}
		case err := <-runErr:
			// Runner exited on its own (usually fatal). Report stopped.
			if err != nil {
				log.Error().Err(err).Msg("runner exited with error")
				status <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			log.Info().Msg("runner exited cleanly")
			break loop
		}
	}

	// Wait for runner to finish (it should after ctx cancel).
	select {
	case err := <-runErr:
		if err != nil {
			log.Warn().Err(err).Msg("runner returned error during shutdown")
		}
	case <-time.After(stopGracePeriod):
		log.Warn().Dur("grace", stopGracePeriod).Msg("runner did not exit within grace period; forcing stopped state")
	}

	status <- svc.Status{State: svc.Stopped}
	return false, 0
}
