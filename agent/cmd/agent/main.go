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
	"github.com/worktrack/agent/internal/heartbeat"
	"github.com/worktrack/agent/internal/lock"
	"github.com/worktrack/agent/internal/sysinfo"
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

	executor := command.NewExecutor(client, time.Duration(cfg.CommandPollSec)*time.Second)
	hbLoop := heartbeat.NewLoop(client, time.Duration(cfg.HeartbeatSec)*time.Second, Version, executor, aiLauncher)
	aiUpdater := aiupdate.NewUpdater(client, dataDir, 30*time.Minute)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hbLoop.Run(rootCtx)
	go executor.Run(rootCtx)
	go aiUpdater.Run(rootCtx)

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
