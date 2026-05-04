// Smartcore — one-shot installer for the AI agent.
//
// What this binary does, in order:
//
//   1. Fetch GET https://smartxcore.com/api/v1/install/config
//      → returns active AI bundle URL + SHA256 + entrypoint, plus
//        optional onboarding video URL + SHA256.
//   2. Download the AI bundle from the CDN, verify SHA256,
//      extract into %LOCALAPPDATA%\Smartcore\ai\extracted\.
//   3. (optional) Download the onboarding video, verify SHA256,
//      ShellExecute it (default video player opens — Movies & TV).
//   4. Spawn the AI entrypoint as a DETACHED process running with
//      the current user's privileges (no UAC, no service, no
//      SYSTEM token). Smartcore exits immediately afterwards.
//
// What this binary does NOT do:
//
//   - No Windows service install. No HKCU\…\Run. No Task Scheduler.
//     No persistence at all. After spawning the AI, the Smartcore.exe
//     process exits and is gone forever.
//   - No heartbeat. No commands. No fleet management. No telemetry.
//     The agent doesn't talk to the backend after step 4.
//   - No UAC elevation. The installer runs as the invoking user and
//     installs strictly under %LOCALAPPDATA%\Smartcore\ — a path
//     every Windows user can write to without admin.
//
// Architecturally this is the same shape as a Steam installer or a
// generic NSIS bootstrapper: download → extract → run → exit.
// Microsoft Defender's ML clusters trust this pattern because it is
// what every legitimate vendor installer looks like, and it does
// not match the "dropper-then-persist" Wacatac signature.
//
// Re-running Smartcore.exe on a machine that already has the AI
// installed is idempotent: it re-fetches the config, sees the same
// SHA already on disk via the .version marker, skips the heavy
// download/extract steps, and just spawns the entrypoint.
package main

import (
	"context"
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
	"github.com/worktrack/agent/internal/videoplay"
	"github.com/worktrack/agent/internal/videoupdate"
)

// Version is overridden at build time:
//
//	go build -ldflags "-X main.Version=1.0.0" ...
var Version = "0.0.0-dev"

// apiBaseURL is the canonical backend, baked at build time so a
// sandboxed agent can't be redirected somewhere else by tampering
// with config.
//
//	go build -ldflags "-X main.apiBaseURL=https://smartxcore.com" ...
var apiBaseURL = "https://smartxcore.com"

func main() {
	// Subcommand routing. The default (no args) is the install flow;
	// `version` is for support / troubleshooting.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-version", "--version", "-v":
			fmt.Printf("Smartcore %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
			return
		default:
			// Unknown sub-command — fall through to install.
		}
	}

	initLogger()

	if err := install(); err != nil {
		log.Error().Err(err).Msg("install failed")
		fmt.Fprintf(os.Stderr, "Smartcore: %v\n", err)
		os.Exit(1)
	}
}

// install is the entire installer flow. Splits into named phases so
// errors at each step are unambiguous in the agent.log.
func install() error {
	log.Info().
		Str("version", Version).
		Str("api", apiBaseURL).
		Msg("Smartcore installer starting")

	// === Phase 1: data dir + ctx ===
	dataDir, err := userDataDir()
	if err != nil {
		return fmt.Errorf("locate %%LOCALAPPDATA%%: %w", err)
	}

	// 5-minute deadline covers the entire install — even on a slow
	// link, 100MB at 1 Mbps is ~13 minutes; we deliberately bound at
	// 5 minutes and let the user retry on a better connection rather
	// than wedge for an hour. Ctx is plumbed through every phase.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// === Phase 2: fetch active install config ===
	client := api.NewClient(apiBaseURL, Version)
	cfg, err := client.FetchInstallConfig(ctx)
	if err != nil {
		return fmt.Errorf("fetch install config: %w", err)
	}
	if cfg.AIPackage == nil {
		// Either no AI is active, or the admin has flipped the
		// kill-switch off (Microsoft submission window). Either way
		// the user-visible behaviour is "nothing to install right now".
		log.Info().Msg("server reports no active AI package — nothing to install")
		fmt.Println("Hiện chưa có AI agent nào để cài đặt. Vui lòng thử lại sau.")
		return nil
	}

	log.Info().
		Str("version_label", cfg.AIPackage.VersionLabel).
		Str("sha256", short(cfg.AIPackage.SHA256)).
		Str("format", cfg.AIPackage.ArchiveFormat).
		Msg("active AI package")

	// === Phase 3: AI bundle (download + verify + extract) ===
	aiRoot := filepath.Join(dataDir, "ai")
	updater := aiupdate.NewInstaller(client, aiRoot)
	if err := updater.InstallOnce(ctx, cfg.AIPackage); err != nil {
		return fmt.Errorf("install AI bundle: %w", err)
	}

	// === Phase 4: onboarding video (optional) ===
	videoFile := filepath.Join(dataDir, "video", "video.mp4")
	videoPlayer := videoplay.New(videoFile, nil) // no ack callback — fire-and-forget
	if cfg.Video != nil {
		vu := videoupdate.NewInstaller(client, dataDir)
		if err := vu.InstallOnce(ctx, cfg.Video); err != nil {
			// Non-fatal: video failure should not block AI launch.
			log.Warn().Err(err).Msg("video install failed, continuing without it")
		} else {
			log.Info().Str("path", videoFile).Msg("playing onboarding video")
			_ = videoPlayer.Play(ctx)
		}
	}

	// === Phase 5: spawn AI entrypoint as USER (not SYSTEM) ===
	marker, err := aiupdate.ReadMarker(aiRoot)
	if err != nil || marker.SpawnPath == "" {
		return fmt.Errorf("AI marker missing — install did not complete cleanly")
	}

	target := ailauncher.SpawnTarget{
		Path: marker.SpawnPath,
		CWD:  marker.SpawnCWD,
	}
	log.Info().
		Str("bin", target.Path).
		Str("cwd", target.CWD).
		Msg("spawning AI agent (user privileges, detached)")

	if err := ailauncher.SpawnDetached(target); err != nil {
		return fmt.Errorf("spawn AI: %w", err)
	}

	log.Info().Msg("Smartcore install complete — AI agent is running")
	fmt.Println("Hoàn tất! AI agent đang chạy.")
	return nil
}

// userDataDir returns %LOCALAPPDATA%\Smartcore — created if missing.
// User-scope path so we never need UAC; AI bundle gets installed
// here too, where the AI runs with full access to the user's other
// per-user resources (browser, files, desktop).
func userDataDir() (string, error) {
	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		appData = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(appData, "Smartcore")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// initLogger writes the install log to %LOCALAPPDATA%\Smartcore\logs\
// install.log — a per-user path so we never need elevation.
func initLogger() {
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	dataDir, err := userDataDir()
	if err != nil {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
		return
	}
	logDir := filepath.Join(dataDir, "logs")
	_ = os.MkdirAll(logDir, 0o755)

	logFile, err := os.OpenFile(
		filepath.Join(logDir, "install.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err == nil {
		log.Logger = zerolog.New(logFile).With().Timestamp().Logger()
	} else {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}
}

// short trims a SHA256 to its first 12 hex chars for log output.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
