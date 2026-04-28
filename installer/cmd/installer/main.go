//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	code, apiBase, err := promptForInputs()
	if err != nil {
		return err
	}

	dataDir, err := dataDir()
	if err != nil {
		return err
	}

	// Stop any prior agent instance so the agent.exe payload write below
	// is not blocked by a held file handle. taskkill is a no-op (logged)
	// when no matching process exists.
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

func resolveAPIBase() string {
	if v := os.Getenv(apiBaseEnv); v != "" {
		return v
	}
	return defaultAPIBase
}

// === GUI helpers (delegated to gui_windows.go for a single-window flow) ===

func promptForInputs() (code, apiBase string, err error) {
	apiBase = resolveAPIBase()
	code, err = showInstallDialog(apiBase)
	if err != nil {
		return "", "", err
	}
	if code == "" {
		return "", "", errors.New("user cancelled")
	}
	return code, apiBase, nil
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
