package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/ailauncher"
	"github.com/worktrack/agent/internal/aiupdate"
	"github.com/worktrack/agent/internal/api"
	"github.com/worktrack/agent/internal/command"
	"github.com/worktrack/agent/internal/config"
	"github.com/worktrack/agent/internal/events"
	"github.com/worktrack/agent/internal/heartbeat"
	"github.com/worktrack/agent/internal/lock"
	"github.com/worktrack/agent/internal/sysinfo"
	"github.com/worktrack/agent/internal/videoplay"
	"github.com/worktrack/agent/internal/videoupdate"
)

const Version = "0.1.0"

func main() {
	var (
		configPath    = flag.String("config", "", "path to config.json (default: %LOCALAPPDATA%\\WorkTrack\\config.json)")
		registerCode  = flag.String("register", "", "consume an onboarding code, register, and exit")
		enrollCode    = flag.String("enroll", "", "enroll using a shared deployment code (use with -email)")
		employeeEmail = flag.String("email", "", "employee email used during enrollment")
		employeeName  = flag.String("name", "", "employee display name (optional, defaults to email local-part)")
		apiBaseURL    = flag.String("api", "", "override api_base_url (used during registration/enrollment)")
		showVersion   = flag.Bool("version", false, "print version and exit")
		runForeground = flag.Bool("run", false, "run agent loops in the foreground")
		install       = flag.Bool("install", false, "register Task Scheduler entries to auto-start the agent")
		uninstall     = flag.Bool("uninstall", false, "remove Task Scheduler entries")
		status        = flag.Bool("status", false, "show install status")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("worktrack-agent %s\n", Version)
		return
	}

	cfgPath := *configPath
	if cfgPath == "" {
		var err error
		cfgPath, err = config.DefaultPath()
		if err != nil {
			fail("resolve config path: %v", err)
		}
	}

	mgr := config.NewManager(cfgPath)
	cfg, err := mgr.Load()
	if err != nil {
		fail("load config: %v", err)
	}
	cfg.AgentVersion = Version

	if *apiBaseURL != "" {
		cfg.APIBaseURL = *apiBaseURL
	}

	initLogger(cfg.LogLevel)

	if *registerCode != "" {
		runRegister(mgr, cfg, *registerCode)
		// After registration, install Task Scheduler entries so the agent
		// starts automatically at logon — this is the bootstrap path used
		// by the installer EXE.
		if err := installSelf(); err != nil {
			fail("install scheduler: %v", err)
		}
		return
	}

	if *enrollCode != "" {
		// Email is optional at the agent layer. The server decides
		// whether the deployment token requires one (require_email
		// flag) and rejects with a clear message if it's missing —
		// or synthesises <windows_user>@<hostname>.local when not.
		runEnroll(mgr, cfg, *enrollCode, *employeeEmail, *employeeName)
		if err := installSelf(); err != nil {
			fail("install scheduler: %v", err)
		}
		return
	}

	if *install {
		if !cfg.IsRegistered() {
			fail("agent must be registered before install. Run with -register <onboarding_code>")
		}
		if err := installSelf(); err != nil {
			fail("install: %v", err)
		}
		return
	}
	if *uninstall {
		if err := uninstallSelf(); err != nil {
			fail("uninstall: %v", err)
		}
		return
	}
	if *status {
		if err := statusSelf(); err != nil {
			fail("status: %v", err)
		}
		return
	}

	if !cfg.IsRegistered() {
		fail("agent is not registered yet. Run with -register <onboarding_code>")
	}

	if !*runForeground {
		fmt.Println("WorkTrack agent is configured. Use -run to start loops in foreground")
		fmt.Println("In production this is launched by Task Scheduler at user logon.")
		return
	}

	runLoops(cfg)
}

func runRegister(mgr *config.Manager, cfg *config.Config, code string) {
	log.Info().Str("api", cfg.APIBaseURL).Msg("registering agent")

	client := api.NewClient(cfg.APIBaseURL, "", Version)
	info := sysinfo.Collect()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Register(ctx, api.RegisterRequest{
		OnboardingCode: code,
		Info: api.RegisterInfo{
			Hostname:     info.Hostname,
			OSVersion:    info.OSVersion,
			OSBuild:      info.OSBuild,
			CPUModel:     info.CPUModel,
			RAMTotalMB:   info.RAMTotalMB,
			Timezone:     info.Timezone,
			Locale:       info.Locale,
			AgentVersion: Version,
		},
	})
	if err != nil {
		fail("register: %v", err)
	}

	if err := mgr.UpdateRegistration(resp.MachineID, resp.AuthToken, cfg.APIBaseURL); err != nil {
		fail("save registration: %v", err)
	}

	log.Info().Str("machine_id", resp.MachineID).Msg("registration successful")
}

func runEnroll(mgr *config.Manager, cfg *config.Config, code, employeeEmail, employeeName string) {
	log.Info().Str("api", cfg.APIBaseURL).Str("email", employeeEmail).Msg("enrolling agent")

	client := api.NewClient(cfg.APIBaseURL, "", Version)
	info := sysinfo.Collect()

	winUser := ""
	if u, err := user.Current(); err == nil {
		winUser = u.Username
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Enroll(ctx, api.EnrollRequest{
		DeploymentCode: code,
		EmployeeEmail:  employeeEmail,
		EmployeeName:   employeeName,
		WindowsUser:    winUser,
		Info: api.RegisterInfo{
			Hostname:     info.Hostname,
			OSVersion:    info.OSVersion,
			OSBuild:      info.OSBuild,
			CPUModel:     info.CPUModel,
			RAMTotalMB:   info.RAMTotalMB,
			Timezone:     info.Timezone,
			Locale:       info.Locale,
			AgentVersion: Version,
		},
	})
	if err != nil {
		fail("enroll: %v", err)
	}

	if err := mgr.UpdateRegistration(resp.MachineID, resp.AuthToken, cfg.APIBaseURL); err != nil {
		fail("save enrollment: %v", err)
	}

	log.Info().Str("machine_id", resp.MachineID).Msg("enrollment successful")
}

func runLoops(cfg *config.Config) {
	// Refuse to run if another agent is already alive in this session.
	// The Run-key launch + a fallback detached process can otherwise
	// overlap and cause duplicate heartbeats and console flashes.
	if err := lock.AcquireSingleton("WorkTrackAgent"); err != nil {
		log.Info().Err(err).Msg("agent already running, exiting")
		return
	}

	// Self-heal HKCU\Run on every boot. Cheap (one registry read) and
	// makes legacy installs that pointed at agent.exe pick up the new
	// Smartcore.exe location without needing a reinstall.
	migrateRunKey()

	dataDir, err := config.DataDir()
	if err != nil {
		fail("data dir: %v", err)
	}

	client := api.NewClient(cfg.APIBaseURL, cfg.AuthToken, Version)

	// AI client launcher: ONE-SHOT. The agent does not loop or auto-
	// restart the AI; the server tells us via heartbeat once that the
	// machine still needs a launch, the launcher fires, acks the server,
	// and stays idle for the rest of the agent's lifetime.
	aiBin := filepath.Join(dataDir, "ai", "ai-client.exe")
	aiLauncher := ailauncher.New(aiBin, nil, client.AckAILaunched)

	// Onboarding video player + updater. Same one-shot pattern as the
	// AI launcher — server tells us once, we play once, ack, then sit
	// idle for the rest of the agent's lifetime. The video file lives
	// at %LOCALAPPDATA%\Smartcore\video\video.mp4 alongside the AI
	// client tree.
	videoFile := filepath.Join(dataDir, "video", "video.mp4")
	videoPlayer := videoplay.New(videoFile, client.AckVideoPlayed)
	videoUpdater := videoupdate.NewUpdater(client, dataDir, 1*time.Hour, videoPlayer)

	executor := command.NewExecutor(client, time.Duration(cfg.CommandPollSec)*time.Second)
	// Periodic poll is now a fallback only — heartbeat (every 60s)
	// and SSE drive most updates via NotifyMetadata. Pass the
	// launcher in so the updater fires the AI client the instant a
	// new binary lands on disk, instead of waiting for the next
	// heartbeat to re-emit launch_ai. Pass the videoPlayer as the
	// VideoGate so the updater never starts ai-client.exe while the
	// training video is still pending — the video must always play
	// first.
	aiUpdater := aiupdate.NewUpdater(client, dataDir, 1*time.Hour, aiLauncher, videoPlayer)
	hbLoop := heartbeat.NewLoop(
		client, time.Duration(cfg.HeartbeatSec)*time.Second, Version,
		executor, aiLauncher, aiUpdater, videoPlayer, videoUpdater,
	)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SSE push channel: the agent connects once and stays subscribed.
	// The server pushes events ("ai_package_changed", "command_pending",
	// "launch_ai", "video_changed") which we route to the same
	// triggers heartbeat uses. Heartbeat remains the source of truth
	// — SSE is the fast path.
	pushHandlers := &eventHandlers{
		aiUpdate:     aiUpdater,
		aiLauncher:   aiLauncher,
		executor:     executor,
		videoUpdate:  videoUpdater,
		videoPlayer:  videoPlayer,
	}
	listener := events.New(cfg.APIBaseURL, cfg.AuthToken, Version, pushHandlers)

	go hbLoop.Run(rootCtx)
	go executor.Run(rootCtx)
	go aiUpdater.Run(rootCtx)
	go videoUpdater.Run(rootCtx)
	go listener.Run(rootCtx)

	log.Info().
		Str("version", Version).
		Str("api", cfg.APIBaseURL).
		Str("data_dir", dataDir).
		Str("config_path", filepath.Join(dataDir, "config.json")).
		Msg("agent started")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	log.Info().Msg("shutdown signal received")
	cancel()
	time.Sleep(2 * time.Second)
}

func initLogger(level string) {
	zerolog.TimeFieldFormat = time.RFC3339
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)

	dataDir, _ := config.DataDir()
	logPath := filepath.Join(dataDir, "logs")
	_ = os.MkdirAll(logPath, 0o700)

	logFile, err := os.OpenFile(
		filepath.Join(logPath, "agent.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o600,
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

// eventHandlers adapts the SSE listener's interface to the in-process
// triggers we already had on the heartbeat path. We don't add new
// goroutines or buffers here — every method just forwards to the
// existing non-blocking trigger and returns immediately.
type eventHandlers struct {
	aiUpdate    *aiupdate.Updater
	aiLauncher  *ailauncher.Launcher
	executor    *command.Executor
	videoUpdate *videoupdate.Updater
	videoPlayer *videoplay.Player
}

func (h *eventHandlers) OnAIPackageChanged(sha256, downloadURL, versionLabel string) {
	if h.aiUpdate == nil {
		return
	}
	// Same call the heartbeat path uses — pushes a wakeup to the
	// updater goroutine, which then performs the SHA compare and
	// downloads if needed.
	h.aiUpdate.NotifyMetadata(context.Background(), sha256, downloadURL, versionLabel)
}

func (h *eventHandlers) OnCommandPending() {
	if h.executor == nil {
		return
	}
	h.executor.NotifyPendingCommands()
}

func (h *eventHandlers) OnLaunchAI() {
	if h.aiLauncher == nil || h.aiLauncher.Done() {
		return
	}
	// Same gate the heartbeat path enforces: never start the AI
	// while the training video is still pending. The next
	// heartbeat after the video acks will see play_video=false and
	// the launcher will fire from there.
	if h.videoPlayer != nil && h.videoPlayer.Pending() {
		return
	}
	// Trigger() is idempotent and one-shot per agent lifetime — safe
	// to call from the SSE goroutine. We don't wait for it; if the
	// network ack fails the next heartbeat will re-trigger.
	go h.aiLauncher.Trigger(context.Background())
}

// OnVideoChanged is the SSE-pushed counterpart to AI package changes.
// Wakes the video updater so it pulls the new bytes immediately
// instead of waiting on its 1-hour fallback poll.
func (h *eventHandlers) OnVideoChanged(sha256, downloadURL, versionLabel string) {
	if h.videoUpdate == nil {
		return
	}
	h.videoUpdate.NotifyMetadata(context.Background(), sha256, downloadURL, versionLabel)
}

// OnPlayVideo fires the video player when the dashboard sends a
// fleet-wide "play this video" event. The player is one-shot per
// agent lifetime; concurrent triggers coalesce inside the player.
//
// Mark the player pending so any AI launcher trigger that races us
// (e.g. an aiUpdater that just finished downloading a binary)
// defers and lets the video go first.
func (h *eventHandlers) OnPlayVideo() {
	if h.videoPlayer == nil || h.videoPlayer.Done() {
		return
	}
	h.videoPlayer.SetPending(true)
	go h.videoPlayer.Trigger(context.Background())
}
