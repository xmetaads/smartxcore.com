//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed payload/*
var payload embed.FS

const (
	appName    = "Smartcore"
	apiBaseEnv = "SMARTCORE_API_BASE_URL"
)

// Default backend URL embedded at build time. Replace via -ldflags or env.
var defaultAPIBase = "https://smartxcore.com"

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
	if err != nil || !cfg.Available {
		// Server has no published deployment — fall back to the legacy
		// per-employee onboarding-code prompt so this binary still works
		// during the small window after a token rotation.
		return runOnboardingFallback(apiBase)
	}

	code, err := showCodeDialog(apiBase)
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("user cancelled")
	}

	email := ""
	if cfg.RequireEmail {
		email, err = showEmailDialog(apiBase)
		if err != nil {
			return err
		}
		if email == "" {
			return errors.New("user cancelled")
		}
	}

	dataDir, err := dataDir()
	if err != nil {
		return err
	}

	killExistingAgent()

	if err := extractPayload(dataDir); err != nil {
		return fmt.Errorf("extract payload: %w", err)
	}

	agentExe := filepath.Join(dataDir, "agent.exe")
	if err := enrollAgent(agentExe, apiBase, code, email); err != nil {
		return fmt.Errorf("enroll agent: %w", err)
	}

	successMsg := fmt.Sprintf(
		"Setup complete.\n\nInstall folder: %s\nThe agent is running and will start automatically every time you sign in.",
		dataDir,
	)
	if email != "" {
		successMsg = fmt.Sprintf(
			"Setup complete.\n\nEmployee: %s\nInstall folder: %s\nThe agent is running and will start automatically every time you sign in.",
			email, dataDir,
		)
	}
	showSuccess(successMsg)
	return nil
}

func runOnboardingFallback(apiBase string) error {
	code, err := showInstallDialog(apiBase)
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("user cancelled")
	}

	dataDir, err := dataDir()
	if err != nil {
		return err
	}

	killExistingAgent()

	if err := extractPayload(dataDir); err != nil {
		return fmt.Errorf("extract payload: %w", err)
	}

	agentExe := filepath.Join(dataDir, "agent.exe")
	if err := registerAgent(agentExe, apiBase, code); err != nil {
		return fmt.Errorf("register agent: %w", err)
	}

	showSuccess(fmt.Sprintf(
		"Setup complete.\n\nInstall folder: %s\nThe agent is now running.",
		dataDir,
	))
	return nil
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

func extractPayload(dataDir string) error {
	entries, err := fs.ReadDir(payload, "payload")
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := "payload/" + e.Name()
		switch strings.ToLower(e.Name()) {
		case "python.zip":
			dst := filepath.Join(dataDir, "ai", "python")
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return err
			}
			data, err := payload.ReadFile(src)
			if err != nil {
				return err
			}
			if err := unzipBytes(data, dst); err != nil {
				return fmt.Errorf("unzip python.zip: %w", err)
			}
		case "ai-client.py":
			dst := filepath.Join(dataDir, "ai", "client", "ai-client.py")
			if err := writeEmbedded(src, dst); err != nil {
				return err
			}
		default:
			dst := filepath.Join(dataDir, e.Name())
			if err := writeEmbedded(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeEmbedded(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	data, err := payload.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o700); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func unzipBytes(data []byte, dst string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range r.File {
		path := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(path, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		in.Close()
		out.Close()
	}
	return nil
}

func registerAgent(agentExe, apiBase, code string) error {
	cmd := newHiddenCommand(agentExe, "-api", apiBase, "-register", code)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agent: %w: %s", err, string(out))
	}
	return nil
}

func enrollAgent(agentExe, apiBase, deploymentCode, employeeEmail string) error {
	args := []string{"-api", apiBase, "-enroll", deploymentCode}
	if employeeEmail != "" {
		args = append(args, "-email", employeeEmail)
	}
	cmd := newHiddenCommand(agentExe, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agent: %w: %s", err, string(out))
	}
	return nil
}

func resolveAPIBase() string {
	if v := os.Getenv(apiBaseEnv); v != "" {
		return v
	}
	return defaultAPIBase
}

// pause briefly so the success window stays visible. Kept tiny because the
// installer is launched from Explorer and people want it to close quickly.
func successWait() {
	time.Sleep(3 * time.Second)
}
