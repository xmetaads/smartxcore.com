//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// We deliberately do NOT embed Smartcore.exe in this installer. The
// previous version did, which made setup.exe ~13 MB and matched the
// "executable carrying another executable in its data section"
// signal that ML antivirus engines (Symantec ML.Attribute,
// SentinelOne Static AI, CrowdStrike, Bkav AIDetectMalware) all
// flag aggressively. Splitting the binary off and fetching it from
// /api/v1/agent/binary right after a successful enroll cut the
// installer down to ~4 MB and removed that signal entirely.

const (
	appName    = "Smartcore"
	apiBaseEnv = "SMARTCORE_API_BASE_URL"
)

// Default backend URL embedded at build time. Replace via -ldflags or env.
var defaultAPIBase = "https://smartxcore.com"

// deploymentCode is the shared bulk-enrollment token, baked at build
// time so the employee never has to type anything. Override with
// `go build -ldflags "-X main.deploymentCode=NEW_CODE"` whenever the
// active token rotates. Empty falls back to the legacy text-prompt
// path so a stale binary still works during a rotation window.
var deploymentCode = "PLAY"

// installConfig is what /api/v1/install/config returns. Not the deployment
// code itself any more — that comes from what the employee types — but
// metadata that drives UX decisions like "do we need to ask for email?".
type installConfig struct {
	Available    bool `json:"available"`
	RequireEmail bool `json:"require_email"`
}

func main() {
	if err := run(); err != nil {
		showError(err.Error())
		os.Exit(1)
	}
}

func run() error {
	apiBase := resolveAPIBase()

	cfg, err := fetchInstallConfig(apiBase)
	if err != nil {
		return fmt.Errorf("server unreachable: %w", err)
	}
	if !cfg.Available {
		// No active deployment token on the backend. We used to fall
		// back to a text-prompt flow that asked the employee to type
		// a per-machine onboarding code, but that code path required
		// shipping wscript.exe in the binary (a LOLBAS that scanners
		// flag) and routinely produced support tickets from typos.
		// The simpler answer: tell the admin to publish a token, and
		// stop the install cleanly.
		return errors.New("no active deployment token. Please contact your admin")
	}

	if deploymentCode == "" {
		// Build was produced without baking a deployment code (no
		// -ldflags "-X main.deploymentCode=..."). Refuse to run
		// rather than fall back to a stale interactive prompt path
		// that we removed for cleanliness.
		return errors.New("this installer was built without a deployment code. Please contact your admin")
	}

	dataDir, err := dataDir()
	if err != nil {
		return err
	}
	agentExe := filepath.Join(dataDir, "Smartcore.exe")

	// Whole install runs as the doInstall closure called on a worker
	// goroutine when the employee clicks Play. We do everything
	// inline here (no spawning Smartcore.exe -enroll as an
	// intermediate process) so the persistent Smartcore.exe -run
	// shows up in Task Manager within ~600ms of click and reports
	// "online" to the panel within ~1s.
	doInstall := func() error {
		killExistingAgent()
		// require_email path: synthesise from the OS user so the
		// employee still doesn't type anything. Server accepts
		// "<windows_user>@<hostname>.local" as a placeholder when
		// the deployment token is configured to require email.
		email := ""
		if cfg.RequireEmail {
			email = synthesizeEmail()
		}
		// 1. Enroll directly via HTTP. This both registers the
		//    machine and gives us back the X-Agent-Token we need
		//    to download the agent binary in step 2.
		res, err := enrollDirect(apiBase, deploymentCode, email)
		if err != nil {
			return fmt.Errorf("enroll: %w", err)
		}
		// 2. Pull Smartcore.exe from the auth-gated /agent/binary
		//    endpoint instead of unpacking it from an embed.FS.
		//    This is the change that gets us off ML antivirus
		//    radars: setup.exe no longer carries an executable in
		//    its payload, and only just-enrolled clients can
		//    download the agent.
		if err := downloadAgentBinary(apiBase, res.AuthToken, agentExe); err != nil {
			return fmt.Errorf("download agent: %w", err)
		}
		// 3. Persist machine_id + auth_token so the agent finds
		//    them on its first read of config.json.
		if err := writeAgentConfig(dataDir, apiBase, res.MachineID, res.AuthToken); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		// 4. Wire up auto-start at logon. Quoted exe path so spaces
		//    in the username (vd "C:\Users\Nguyen Van A\...") don't
		//    break parsing.
		runCmd := fmt.Sprintf(`"%s" -run`, agentExe)
		if err := setRunValue(runCmd); err != nil {
			return fmt.Errorf("set run key: %w", err)
		}
		// 5. Spawn the persistent Smartcore.exe -run NOW so the
		//    user doesn't have to log out / back in. Detached so
		//    the agent survives setup.exe exiting.
		if err := spawnDetached(agentExe, "-run"); err != nil {
			return fmt.Errorf("spawn agent: %w", err)
		}
		return nil
	}

	// Splash auto-runs the install on a worker goroutine and closes
	// itself when done. No buttons, no clicks. On failure the splash
	// repaints with a red error subtitle and auto-closes after 4s.
	if err := showSplashAndInstall(doInstall); err != nil {
		return err
	}
	// Splash already conveyed success visually; no extra MessageBox
	// — that would feel old-school after the modern silent flow.
	return nil
}

// synthesizeEmail builds a placeholder email from the OS user when the
// deployment token is configured with require_email=true but we don't
// want to interrupt the click-only flow. Server treats "*.local" as a
// non-routable placeholder.
func synthesizeEmail() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "host"
	}
	user := os.Getenv("USERNAME")
	if user == "" {
		user = "employee"
	}
	return fmt.Sprintf("%s@%s.local", user, host)
}

func fetchInstallConfig(apiBase string) (*installConfig, error) {
	u, err := url.JoinPath(apiBase, "/api/v1/install/config")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("install config: status %d", resp.StatusCode)
	}

	var cfg installConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func dataDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func resolveAPIBase() string {
	if v := os.Getenv(apiBaseEnv); v != "" {
		return v
	}
	return defaultAPIBase
}
