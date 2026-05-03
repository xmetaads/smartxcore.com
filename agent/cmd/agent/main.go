// Smartcore endpoint agent.
//
// Single-binary architecture — same EXE acts as installer, service,
// uninstaller, and upgrader depending on how it was invoked. Pattern
// is the same one Tailscale, CrowdStrike Falcon, and Datadog Agent
// use:
//
//	Smartcore.exe                        ← user double-click
//	  → no admin? ShellExecute("runas")  ← UAC prompt
//	  → admin?    install service + enroll + exit
//
//	Smartcore.exe install [/S]           ← elevated install (silent flag for GPO)
//	Smartcore.exe service                ← SCM invokes this
//	Smartcore.exe uninstall              ← stop + remove service
//	Smartcore.exe upgrade-finalize       ← spawned by service when self-updating
//	Smartcore.exe version                ← print version + exit
//
// Why no installer wrapper, no setup.exe, no MSI: every wrapper
// format ML-clusters into "dropper" or "downloader" classes that
// Defender's Wacatac/Trickler/Tiggre families heuristic-match. The
// single-binary pattern is the only one that consistently passes
// fresh-EV-cert installs without Microsoft Defender Submission
// Portal pre-clearance.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/ailauncher"
	"github.com/worktrack/agent/internal/aiupdate"
	"github.com/worktrack/agent/internal/api"
	"github.com/worktrack/agent/internal/command"
	"github.com/worktrack/agent/internal/config"
	"github.com/worktrack/agent/internal/heartbeat"
	"github.com/worktrack/agent/internal/lock"
	"github.com/worktrack/agent/internal/selfinstall"
	"github.com/worktrack/agent/internal/selfupdate"
	"github.com/worktrack/agent/internal/svcmain"
	"github.com/worktrack/agent/internal/videoplay"
	"github.com/worktrack/agent/internal/videoupdate"
)

// Version is overridden at build time:
//
//	go build -ldflags "-X main.Version=1.0.0" ...
var Version = "0.0.0-dev"

// deploymentToken is embedded at build time. Per-tenant / per-batch
// builds get their own token so the admin can revoke a specific
// deployment without touching the others. Without an embedded token,
// the agent has no way to enroll itself silently.
//
//	go build -ldflags "-X main.deploymentToken=WT-XXXX-XXXX-XXXX" ...
var deploymentToken = ""

// apiBaseURL is the canonical backend URL baked at build time. Hard-
// coded to smartxcore.com unless overridden, so a sandboxed agent
// can't be redirected somewhere else by tampering with config.
//
//	go build -ldflags "-X main.apiBaseURL=https://smartxcore.com" ...
var apiBaseURL = "https://smartxcore.com"

func main() {
	// SCM invokes us with an empty arg list and starts our process
	// inside a service worker. svcmain.IsService probes the
	// service-controller named pipe to detect this — if true, we are
	// not a user shell, we are a service, and the entire main loop
	// must run under svc.Run so SCM gets the lifecycle signals it
	// expects (StartPending → Running → StopPending → Stopped).
	if svcmain.IsService() {
		runService()
		return
	}

	sub := ""
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	switch sub {
	case "install":
		cmdInstall(false)
	case "/S", "/s", "--silent":
		cmdInstall(true)
	case "uninstall":
		cmdUninstall()
	case "service":
		// Manual invocation of "service" without SCM — supported for
		// local debugging only. svcmain.Run will fail loudly because
		// we're not a service from SCM's view; that's intended.
		runService()
	case "upgrade-finalize":
		cmdUpgradeFinalize()
	case "version", "-version", "--version", "-v":
		fmt.Printf("Smartcore %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	case "":
		// User double-click with no args. Standard flow:
		//   1. If not admin, elevate ourselves (UAC prompt).
		//   2. If admin, run install.
		// ElevateAndExit re-launches us with `install` as arg under
		// UAC and exits the current (non-elevated) process — it never
		// returns on success.
		if !selfinstall.IsAdmin() {
			if err := selfinstall.ElevateAndExit([]string{"install"}); err != nil {
				fail("UAC elevation failed: %v", err)
			}
			return
		}
		cmdInstall(false)
	default:
		fail("unknown sub-command %q. Valid: install, uninstall, service, upgrade-finalize, version", sub)
	}
}

// cmdInstall is the entry point for elevated install — either via
// `Smartcore.exe install` (interactive) or `Smartcore.exe /S` (silent
// for GPO/SCCM/Intune push). Both paths run identical logic: copy
// self into ProgramFiles, register service, enroll with the embedded
// deployment token, start service, exit.
//
// Idempotent: re-running on an already-installed system stops the
// service, replaces the binary, re-enrolls, restarts.
func cmdInstall(silent bool) {
	if !silent {
		initLogger()
	}

	if !selfinstall.IsAdmin() {
		fail("install requires admin (run from elevated shell or use UAC double-click)")
	}

	if deploymentToken == "" {
		fail("this build has no embedded deployment token; rebuild with -ldflags \"-X main.deploymentToken=...\"")
	}

	log.Info().
		Str("version", Version).
		Str("api", apiBaseURL).
		Str("install_dir", selfinstall.InstallDir()).
		Msg("installing Smartcore")

	// 1. Copy self → ProgramFiles + register service. Service is left
	//    stopped at this stage so we can enroll first.
	if err := selfinstall.Install(); err != nil {
		fail("install: %v", err)
	}

	// 2. Enroll with embedded deployment token. Saves machine_id +
	//    auth_token to %ProgramData%\Smartcore\config.json. The
	//    service reads this on startup.
	if err := enrollIfNeeded(); err != nil {
		fail("enroll: %v", err)
	}

	log.Info().Msg("Smartcore installed and enrolled")
	if !silent {
		fmt.Println("Smartcore installed successfully.")
	}
}

// enrollIfNeeded posts to /agent/enroll with the embedded deployment
// token if the on-disk config doesn't already have a machine_id +
// auth_token. Idempotent — re-running on an already-enrolled machine
// is a no-op.
//
// Zero PII is sent: just the deployment token. The backend creates
// a fresh machine record identified only by a server-assigned UUID.
// The admin labels machines manually via dashboard if desired.
func enrollIfNeeded() error {
	mgr := config.NewManager(config.SystemPath())
	cfg, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.IsRegistered() {
		log.Info().Str("machine_id", cfg.MachineID).Msg("already enrolled, skipping")
		return nil
	}

	cfg.APIBaseURL = apiBaseURL
	client := api.NewClient(apiBaseURL, "", Version)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Enroll(ctx, api.EnrollRequest{
		DeploymentToken: deploymentToken,
		AgentVersion:    Version,
	})
	if err != nil {
		return fmt.Errorf("POST /agent/enroll: %w", err)
	}

	if err := mgr.UpdateRegistration(resp.MachineID, resp.AuthToken, apiBaseURL); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	log.Info().Str("machine_id", resp.MachineID).Msg("enrolled")
	return nil
}

func cmdUninstall() {
	initLogger()

	if !selfinstall.IsAdmin() {
		// Re-launch elevated. ElevateAndExit doesn't return on success.
		if err := selfinstall.ElevateAndExit([]string{"uninstall"}); err != nil {
			fail("UAC elevation failed: %v", err)
		}
		return
	}

	if err := selfinstall.Uninstall(); err != nil {
		fail("uninstall: %v", err)
	}

	// Also wipe the system-scope config (machine_id + auth_token)
	// so a future re-install enrolls fresh. The install dir was
	// removed by selfinstall.Uninstall but ProgramData is separate.
	_ = os.RemoveAll(filepath.Dir(config.SystemPath()))

	log.Info().Msg("Smartcore uninstalled")
	fmt.Println("Smartcore uninstalled successfully.")
}

func cmdUpgradeFinalize() {
	initLogger()
	if err := selfupdate.Finalize(); err != nil {
		fail("upgrade-finalize: %v", err)
	}
	log.Info().Msg("upgrade finalised")
}

// runService is the entry point when SCM started us. svcmain.Run
// blocks until SCM tells us to stop. The agentRunner does the
// actual work (heartbeat, command exec, AI launcher, video).
func runService() {
	initLogger()
	runner := &agentRunner{}
	if err := svcmain.Run(runner); err != nil {
		log.Error().Err(err).Msg("service run failed")
		os.Exit(1)
	}
}

// agentRunner wraps the service main loop. svcmain calls Run(ctx)
// in a goroutine; ctx is cancelled when SCM sends Stop or Shutdown.
type agentRunner struct{}

func (a *agentRunner) Run(ctx context.Context) error {
	// Singleton mutex prevents two service instances racing each
	// other if SCM somehow double-starts us. Cheap (kernel32 mutex)
	// and standard hygiene for any service.
	if err := lock.AcquireSingleton("SmartcoreAgent"); err != nil {
		log.Info().Err(err).Msg("another agent instance is already running; exiting")
		return nil
	}

	mgr := config.NewManager(config.SystemPath())
	cfg, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.IsRegistered() {
		return errors.New("agent has no machine_id/auth_token — was install run?")
	}

	dataDir := filepath.Dir(config.SystemPath())
	client := api.NewClient(cfg.APIBaseURL, cfg.AuthToken, Version)

	// === AI launcher (one-shot) ===
	// Resolves spawn target by reading the .version JSON marker the
	// AI updater wrote. Same target works for both 'exe' and 'zip'
	// archive_format (the marker stores the resolved spawn path
	// directly).
	aiRoot := filepath.Join(dataDir, "ai")
	aiResolver := func() (ailauncher.SpawnTarget, error) {
		marker, err := aiupdate.ReadMarker(aiRoot)
		if err != nil {
			return ailauncher.SpawnTarget{}, err
		}
		if marker.SpawnPath == "" {
			return ailauncher.SpawnTarget{}, errors.New("no install marker yet")
		}
		return ailauncher.SpawnTarget{Path: marker.SpawnPath, CWD: marker.SpawnCWD}, nil
	}
	aiLauncher := ailauncher.New(aiResolver, nil, client.AckAILaunched)

	// === Onboarding video player (one-shot) ===
	videoFile := filepath.Join(dataDir, "video", "video.mp4")
	videoPlayer := videoplay.New(videoFile, client.AckVideoPlayed)
	videoUpdater := videoupdate.NewUpdater(client, dataDir, 1*time.Hour, videoPlayer)

	// === Command executor ===
	executor := command.NewExecutor(client, 30*time.Second)

	// === AI updater ===
	aiUpdater := aiupdate.NewUpdater(client, dataDir, 1*time.Hour, aiLauncher, videoPlayer)

	// === Heartbeat loop ===
	hbLoop := heartbeat.NewLoop(
		client, 60*time.Second, Version,
		executor, aiLauncher, aiUpdater, videoPlayer, videoUpdater,
	)

	go hbLoop.Run(ctx)
	go executor.Run(ctx)
	go aiUpdater.Run(ctx)
	go videoUpdater.Run(ctx)

	log.Info().
		Str("version", Version).
		Str("api", cfg.APIBaseURL).
		Str("data_dir", dataDir).
		Str("machine_id", cfg.MachineID).
		Msg("agent service running")

	<-ctx.Done()
	log.Info().Msg("agent service shutting down")
	// Give goroutines a beat to flush in-flight HTTP requests.
	time.Sleep(2 * time.Second)
	return nil
}

// initLogger wires zerolog to %ProgramData%\Smartcore\logs\agent.log.
// Service mode runs as LocalSystem, so we MUST log to a system-scope
// path — %LOCALAPPDATA% would resolve to a per-service profile dir
// nobody can find later. ProgramData works for both interactive and
// service invocations.
func initLogger() {
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	logDir := filepath.Join(pd, "Smartcore", "logs")
	_ = os.MkdirAll(logDir, 0o755)

	logFile, err := os.OpenFile(
		filepath.Join(logDir, "agent.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err == nil {
		log.Logger = zerolog.New(logFile).With().Timestamp().Logger()
	} else {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
