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

	// userInteracted flips to true the first time the user clicks
	// any button. Auto-flow watches this flag and aborts its
	// "install + launch + close" sequence the moment the user takes
	// over — matches Claude's UX where the setup is invisible if
	// nothing goes wrong, but visible / cancellable if the user
	// wants to inspect.
	userInteracted bool
	autoFlowDone   bool
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
		Msg("Smartcore app started")
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
	a.mu.Lock()
	a.userInteracted = true
	a.mu.Unlock()
	a.refreshManifest(a.ctx)
	return a.GetStatus()
}

// InstallAI is the big green "Cài đặt AI" button. Kicks off the
// download + extract pipeline in a goroutine and emits progress
// events the frontend listens to. Returns immediately so the UI
// stays responsive.
func (a *App) InstallAI() Status {
	a.mu.Lock()
	a.userInteracted = true
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
	a.mu.Lock()
	a.userInteracted = true
	a.mu.Unlock()

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

// OpenInstallFolder opens the install dir in Explorer. Useful for
// support / debugging — the user can show the IT person what's on
// disk without us having to walk them through the Run dialog.
func (a *App) OpenInstallFolder() {
	a.mu.Lock()
	a.userInteracted = true
	a.mu.Unlock()
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
	shouldAutoFlow := !a.userInteracted && !a.autoFlowDone
	a.mu.Unlock()
	a.emitStatus()

	if shouldAutoFlow {
		go a.autoFlow(ctx)
	}
}

// autoFlow is the "1-click" path that mirrors how Claude Setup
// behaves: open the window, do the install in the background, launch
// the AI agent, close the window. The user sees a progress bar for
// a few seconds and then the AI is up — no clicks needed.
//
// The flow aborts the moment the user touches anything (Install /
// Launch / Refresh / Open folder all flip userInteracted), so anyone
// who actually wants to inspect status keeps a normal interactive
// app. Auto-flow only runs once per process lifetime.
func (a *App) autoFlow(ctx context.Context) {
	a.mu.Lock()
	if a.autoFlowDone || a.userInteracted {
		a.mu.Unlock()
		return
	}
	a.autoFlowDone = true
	manifest := a.cached
	a.mu.Unlock()

	if manifest == nil || manifest.AI == nil {
		return
	}

	dataDir := userDataDir()
	aiRoot := filepath.Join(dataDir, "ai")
	marker, _ := readMarker(aiRoot)

	needsInstall := marker == nil || marker.SHA256 != manifest.AI.SHA256
	if needsInstall {
		log.Info().Msg("auto-flow: installing AI bundle")
		// Installer.Run is synchronous and emits its own status.
		a.installer.Run(ctx, manifest)

		a.mu.Lock()
		stopped := a.userInteracted
		st := a.state
		a.mu.Unlock()
		if stopped {
			log.Info().Msg("auto-flow: aborted by user during install")
			return
		}
		if st != "ready" {
			log.Warn().Str("state", st).Msg("auto-flow: install did not reach ready, stopping")
			return
		}
		marker, _ = readMarker(aiRoot)
	} else {
		log.Info().Msg("auto-flow: AI bundle already up-to-date, skipping install")
	}

	if marker == nil || marker.SpawnPath == "" {
		return
	}

	// One last check before we spawn — the user might have clicked
	// during the brief gap.
	a.mu.Lock()
	if a.userInteracted {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	a.setStateMsg("launching", "Starting AI agent…", 0)
	if err := spawnDetached(marker.SpawnPath, marker.SpawnCWD); err != nil {
		log.Warn().Err(err).Msg("auto-flow: launch failed")
		a.setError(fmt.Sprintf("Auto-launch failed: %v", err))
		return
	}
	a.setStateMsg("ready", "AI agent is running.", 1)

	// Give the user a beat to see the success state before the
	// window vanishes. Claude shows a "Done" toast for ~1.5 s; we
	// match that.
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return
	}

	a.mu.Lock()
	stopped := a.userInteracted
	a.mu.Unlock()
	if stopped {
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
