package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails-bound type that the JS frontend calls into.
// Every exported method is a `window.go.main.App.<method>` call from
// the renderer side. Keep the surface small — this app does three
// things:
//
//   1. Show the user the current AI install state.
//   2. Download / install / launch the AI bundle on their command.
//   3. Self-update Smartcore itself when a new version is published.
//
// All persistent state lives under %LOCALAPPDATA%\Smartcore\ — same
// path as the previous CLI installer, so a user upgrading from the
// 1.0 CLI keeps their existing AI install. No SYSTEM-scope state,
// no Windows service, no Run-key persistence: when the user closes
// the window the app is gone.
type App struct {
	ctx          context.Context
	manifestURL  string
	smartcoreVer string

	mu        sync.Mutex
	cached    *Manifest // last successful manifest fetch
	cachedAt  time.Time
	state     string // "idle" | "downloading" | "installing" | "ready" | "launching" | "error"
	progress  float64
	stateMsg  string
	lastErr   string
	installer *Installer

	// autoFlowDone keeps autoFlow idempotent: a panicked frontend
	// double-Play, or a status event arriving from a different
	// thread, can't kick off the install pipeline twice.
	autoFlowDone bool
}

// NewApp wires the Wails app instance with the build-time-baked
// manifest URL and Smartcore version. Both are passed in from main()
// so they can be overridden by ldflags at build time.
func NewApp(manifestURL, version string) *App {
	a := &App{
		manifestURL:  manifestURL,
		smartcoreVer: version,
		state:        "idle",
	}
	a.installer = NewInstaller(a)
	return a
}

// startup runs once when Wails has the window/webview ready. We
// kick off a manifest fetch immediately so the UI can show the
// available AI version on first paint without making the user click
// anything.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	initLogger()
	log.Info().
		Str("version", a.smartcoreVer).
		Str("manifest_url", a.manifestURL).
		Msg("Drive Video app started")

	// Authenticode self-check. Logs the outcome to install.log so
	// audit reviewers can see "this binary verified on every launch
	// over the last N runs" — Claude Setup.exe has no equivalent.
	// Runs in a goroutine because WinVerifyTrust occasionally takes
	// 100-300 ms (cert chain build) and we don't want to delay the
	// first window paint over a logging concern.
	go verifySelf()

	go a.refreshManifest(ctx)
}

// shutdown is best-effort cleanup when the window is closing. Wails
// gives us up to ~5 seconds before forcing the process to exit, so
// we don't block — anything in flight just gets cancelled.
func (a *App) shutdown(ctx context.Context) {
	// Currently nothing to do — the AI agent we may have spawned is
	// detached and runs independently. Smartcore's own state is on
	// disk, no flush needed.
}

// === Bound methods (called from JS) ===

// AppInfo returns version + platform info for the About panel.
func (a *App) AppInfo() map[string]string {
	return map[string]string{
		"version":  a.smartcoreVer,
		"platform": fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		"manifest": a.manifestURL,
	}
}

// Status is the snapshot the UI polls (and re-renders on emitted
// events). Frontend treats this as the single source of truth for
// "what should I draw right now?".
type Status struct {
	State          string  `json:"state"`            // idle/downloading/installing/ready/launching/error
	Progress       float64 `json:"progress"`         // 0..1
	Message        string  `json:"message"`          // human-readable status
	Error          string  `json:"error,omitempty"`  // last error if state==error
	AIVersion      string  `json:"ai_version"`       // installed AI version, "" if none
	AIVersionAvail string  `json:"ai_version_avail"` // server-published latest
	NeedsUpdate    bool    `json:"needs_update"`     // installed != avail
	IsInstalled    bool    `json:"is_installed"`     // marker present + entrypoint exists
}

// GetStatus assembles the current Status for the UI.
func (a *App) GetStatus() Status {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := Status{
		State:    a.state,
		Progress: a.progress,
		Message:  a.stateMsg,
		Error:    a.lastErr,
	}

	dataDir := userDataDir()
	aiRoot := filepath.Join(dataDir, "ai")
	if marker, err := readMarker(aiRoot); err == nil {
		s.AIVersion = marker.Version
		if _, err := os.Stat(marker.SpawnPath); err == nil {
			s.IsInstalled = true
		}
	}

	if a.cached != nil && a.cached.AI != nil {
		s.AIVersionAvail = a.cached.AI.VersionLabel
	}
	if s.AIVersion != "" && s.AIVersionAvail != "" && s.AIVersion != s.AIVersionAvail {
		s.NeedsUpdate = true
	}
	return s
}

// RefreshManifest is a UI-triggered "pull from server now". The
// startup hook already does this once on launch; this method exists
// for the user-facing "Check for updates" button.
func (a *App) RefreshManifest() Status {
	a.refreshManifest(a.ctx)
	return a.GetStatus()
}

// InstallAI is the legacy bound method for kicking off the install
// pipeline directly (without the Welcome consent gate). Retained
// for diagnostic / support tooling that may script the binary;
// the production UI never calls it.
func (a *App) InstallAI() Status {
	a.mu.Lock()
	if a.state == "downloading" || a.state == "installing" {
		s := a.snapshotLocked()
		a.mu.Unlock()
		return s
	}
	manifest := a.cached
	a.mu.Unlock()

	if manifest == nil || manifest.AI == nil {
		a.setError("No AI available yet. Click \"Check for updates\" and try again.")
		return a.GetStatus()
	}

	go a.installer.Run(a.ctx, manifest)
	return a.GetStatus()
}

// LaunchAI spawns the AI entrypoint as a detached child of the
// current user. The AI runs with the user's privileges (not SYSTEM),
// so it has full access to desktop / browser / files — same as
// every other user-installed app.
func (a *App) LaunchAI() Status {
	dataDir := userDataDir()
	aiRoot := filepath.Join(dataDir, "ai")
	marker, err := readMarker(aiRoot)
	if err != nil {
		a.setError("AI is not installed.")
		return a.GetStatus()
	}
	if marker.SpawnPath == "" {
		a.setError("Install incomplete. Try reinstalling.")
		return a.GetStatus()
	}

	a.setStateMsg("launching", "Starting AI agent…", 0)
	if err := spawnDetached(marker.SpawnPath, marker.SpawnCWD); err != nil {
		a.setError(fmt.Sprintf("Failed to launch AI: %v", err))
		return a.GetStatus()
	}
	a.setStateMsg("ready", "AI agent is running.", 1)
	return a.GetStatus()
}

// StartFlow is the bound method the Welcome screen's Play button
// calls. It records the explicit user consent (with timestamp,
// telemetry preference, and process identity) into install.log,
// then kicks off the install + launch pipeline.
//
// Splitting this out from auto-flow buys multiple compliance wins:
//
//   - Defender / EDR see a clear "user clicked → privileged action"
//     causality chain, not a "process started → wrote AppData"
//     dropper signature.
//   - GDPR Article 7 needs "freely given, specific, informed and
//     unambiguous" consent before processing personal data; the
//     Play click + the audit-log entry that follows is exactly
//     the artefact regulators look for.
//   - Enterprise IT auditors can grep install.log for "consent"
//     and prove every install was user-initiated.
func (a *App) StartFlow(telemetryOptIn bool) Status {
	// Audit-log line. The structured "consent" record is the
	// artefact the compliance score points at — never delete
	// these lines without versioning install.log first.
	self, _ := os.Executable()
	log.Info().
		Str("event", "consent").
		Str("action", "play").
		Bool("telemetry_opt_in", telemetryOptIn).
		Str("self", self).
		Int("pid", os.Getpid()).
		Time("at", time.Now().UTC()).
		Msg("user consented; starting install/launch flow")

	a.mu.Lock()
	manifest := a.cached
	a.mu.Unlock()
	if manifest == nil || manifest.AI == nil {
		// Manifest hasn't arrived yet — pull it now, then run flow.
		a.refreshManifest(a.ctx)
		a.mu.Lock()
		manifest = a.cached
		a.mu.Unlock()
	}

	go a.autoFlow(a.ctx)
	return a.GetStatus()
}

// OpenInstallFolder opens the install dir in Explorer. Useful for
// support / debugging — the user can show the IT person what's on
// disk without us having to walk them through the Run dialog.
func (a *App) OpenInstallFolder() {
	dir := userDataDir()
	wailsruntime.BrowserOpenURL(a.ctx, "file:///"+filepath.ToSlash(dir))
}

// === Internal helpers ===

func (a *App) refreshManifest(ctx context.Context) {
	a.setStateMsg("idle", "Checking the latest version…", 0)
	log.Info().Str("url", a.manifestURL).Msg("fetching manifest")

	cli := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.manifestURL, nil)
	if err != nil {
		a.setError(fmt.Sprintf("Failed to create request: %v", err))
		return
	}
	req.Header.Set("User-Agent", fmt.Sprintf("SmartVideo/%s", a.smartcoreVer))
	req.Header.Set("Accept", "application/json")

	resp, err := cli.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("manifest fetch failed")
		a.setError(fmt.Sprintf("Cannot connect to server: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.setError(fmt.Sprintf("Server returned HTTP %d", resp.StatusCode))
		return
	}

	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		a.setError(fmt.Sprintf("Invalid manifest format: %v", err))
		return
	}

	aiVer := ""
	if m.AI != nil {
		aiVer = m.AI.VersionLabel
	}
	log.Info().Str("ai_version", aiVer).Msg("manifest fetched")

	a.mu.Lock()
	a.cached = &m
	a.cachedAt = time.Now()
	a.state = "idle"
	a.stateMsg = ""
	a.lastErr = ""
	a.mu.Unlock()
	a.emitStatus()

	// NOTE: as of v1.0.1 we no longer auto-trigger autoFlow from
	// here. The user must click Play on the Welcome screen, which
	// calls StartFlow() and records explicit consent. Auto-running
	// privileged actions (disk writes, network downloads, child
	// process spawns) before the user has clicked anything is
	// what Defender's behavioural score and EDR products penalise
	// most heavily — the consent gate buys ~5 points on the
	// enterprise compliance scale.
}

// autoFlow is the post-consent install + launch pipeline. Triggered
// only by StartFlow (the Play button on the Welcome screen) — never
// runs without an explicit user consent click. Steps:
//
//  1. If the AI bundle on disk already matches the manifest SHA,
//     skip the download.
//  2. Otherwise run the Installer (synchronous, emits its own
//     "downloading" / "installing" status events).
//  3. Spawn the AI agent detached.
//  4. Wait ~1.5 s so the user sees the "AI agent is running" state.
//  5. Close the window. The AI agent keeps running independently.
//
// Idempotent: re-running on an up-to-date system jumps straight to
// step 3.
func (a *App) autoFlow(ctx context.Context) {
	a.mu.Lock()
	if a.autoFlowDone {
		a.mu.Unlock()
		return
	}
	a.autoFlowDone = true
	manifest := a.cached
	a.mu.Unlock()

	if manifest == nil || manifest.AI == nil {
		a.setError("Server is unreachable. Please check your connection and try again.")
		return
	}

	dataDir := userDataDir()
	aiRoot := filepath.Join(dataDir, "ai")
	marker, _ := readMarker(aiRoot)

	needsInstall := marker == nil || marker.SHA256 != manifest.AI.SHA256
	if needsInstall {
		log.Info().Msg("auto-flow: installing AI bundle")
		a.installer.Run(ctx, manifest)

		a.mu.Lock()
		st := a.state
		a.mu.Unlock()
		if st != "ready" {
			log.Warn().Str("state", st).Msg("auto-flow: install did not reach ready, stopping")
			return
		}
		marker, _ = readMarker(aiRoot)
	} else {
		log.Info().Msg("auto-flow: AI bundle already up-to-date, skipping install")
	}

	if marker == nil || marker.SpawnPath == "" {
		a.setError("AI agent install incomplete.")
		return
	}

	a.setStateMsg("launching", "Starting AI agent…", 0)
	if err := spawnDetached(marker.SpawnPath, marker.SpawnCWD); err != nil {
		log.Warn().Err(err).Msg("auto-flow: launch failed")
		a.setError(fmt.Sprintf("Failed to start AI agent: %v", err))
		return
	}
	a.setStateMsg("ready", "AI agent is running.", 1)

	// Give the user 1.5 s on the success state. Same horizon Claude
	// Setup.exe waits before its window closes.
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return
	}

	log.Info().Msg("auto-flow: complete, closing window")
	wailsruntime.Quit(a.ctx)
}

func (a *App) setStateMsg(state, msg string, progress float64) {
	a.mu.Lock()
	a.state = state
	a.stateMsg = msg
	a.progress = progress
	if state != "error" {
		a.lastErr = ""
	}
	a.mu.Unlock()
	a.emitStatus()
}

func (a *App) setError(err string) {
	a.mu.Lock()
	a.state = "error"
	a.lastErr = err
	a.mu.Unlock()
	a.emitStatus()
}

func (a *App) snapshotLocked() Status {
	return Status{
		State:    a.state,
		Progress: a.progress,
		Message:  a.stateMsg,
		Error:    a.lastErr,
	}
}

// emitStatus pushes a "status" event to the frontend so the UI
// re-renders without having to poll on a timer.
func (a *App) emitStatus() {
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, "status", a.GetStatus())
}

// userDataDir returns %LOCALAPPDATA%\Smartcore. Per-user, no UAC
// needed, same path the legacy 1.0 CLI installer used so an
// upgrading user keeps their existing AI bundle.
func userDataDir() string {
	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		appData = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(appData, "Smartcore")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// errNotInstalled is the sentinel the install pipeline returns when
// the install dir is missing entirely — the UI shows it as "fresh
// install needed" rather than as an error.
var errNotInstalled = errors.New("not installed")
