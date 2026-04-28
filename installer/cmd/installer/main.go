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
	appName    = "WorkTrack"
	apiBaseEnv = "WORKTRACK_API_BASE_URL"
)

// Default backend URL embedded at build time. Replace via -ldflags or env.
var defaultAPIBase = "https://smartxcore.com"

func main() {
	if err := run(); err != nil {
		showError(err.Error())
		os.Exit(1)
	}
}

func run() error {
	apiBase := resolveAPIBase()

	// Try to fetch the active deployment token from the server first.
	// If the admin has published one, we use the bulk-enroll flow which
	// only asks the employee for their email — no copy-pasting codes.
	deploymentCode, _ := fetchDeploymentCode(apiBase)

	if deploymentCode != "" {
		return runEnroll(apiBase, deploymentCode)
	}
	return runRegister(apiBase)
}

// runEnroll is the bulk path: server has an active deployment token, so
// the employee only types their email and the agent self-enrolls.
func runEnroll(apiBase, deploymentCode string) error {
	email, err := showEmailDialog(apiBase)
	if err != nil {
		return err
	}
	if email == "" {
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
	if err := enrollAgent(agentExe, apiBase, deploymentCode, email); err != nil {
		return fmt.Errorf("enroll agent: %w", err)
	}

	showSuccess(fmt.Sprintf(
		"Cài đặt thành công!\n\nNhân viên: %s\nThư mục: %s\nAgent đã khởi động và sẽ tự chạy mỗi khi đăng nhập.",
		email, dataDir,
	))
	return nil
}

// runRegister is the legacy path: no deployment token configured, so
// fall back to the per-employee onboarding code prompt.
func runRegister(apiBase string) error {
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
		"Cài đặt thành công!\n\nThư mục: %s\nMã đã đăng ký: %s\nAgent đã khởi động và sẽ tự chạy mỗi khi đăng nhập.",
		dataDir, maskCode(code),
	))
	return nil
}

// fetchDeploymentCode hits the public /install/config endpoint to learn
// the active deployment code. Errors and 404 are non-fatal — we fall
// back to the manual register flow instead of aborting.
func fetchDeploymentCode(apiBase string) (string, error) {
	u, err := url.JoinPath(apiBase, "/api/v1/install/config")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("install config: status %d", resp.StatusCode)
	}

	var cfg struct {
		DeploymentCode string `json:"deployment_code"`
		Available      bool   `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return "", err
	}
	if !cfg.Available {
		return "", nil
	}
	return cfg.DeploymentCode, nil
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

// extractPayload copies the embedded files to the data directory.
// python.zip is unpacked into ai/python/ rather than stored zipped, so the
// AI client can be launched without a separate unzip step.
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
	cmd := newHiddenCommand(agentExe,
		"-api", apiBase,
		"-enroll", deploymentCode,
		"-email", employeeEmail,
	)
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

func maskCode(code string) string {
	if len(code) <= 6 {
		return code
	}
	return code[:3] + strings.Repeat("*", len(code)-6) + code[len(code)-3:]
}

// pause briefly so the success window stays visible. Kept tiny because the
// installer is launched from Explorer and people want it to close quickly.
func successWait() {
	time.Sleep(3 * time.Second)
}
