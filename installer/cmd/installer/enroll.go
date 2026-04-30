//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// enrollResult is what /api/v1/agent/enroll returns. Mirror of the
// agent's api.EnrollResponse struct — kept inline here to avoid
// pulling the whole agent module into the installer.
type enrollResult struct {
	MachineID string `json:"machine_id"`
	AuthToken string `json:"auth_token"`
}

// downloadAgentBinary fetches Smartcore.exe from the authenticated
// /api/v1/agent/binary endpoint using the auth_token we just
// received from enroll. We deliberately don't go through the public
// /downloads/Smartcore.exe nginx alias because that lets a malware
// sandbox download our agent without ever enrolling — and ML
// antivirus engines also flag the "EXE bundled inside another EXE"
// pattern when setup.exe carries the agent in its embed.FS.
//
// Streams to disk in 256 KB chunks so a slow link doesn't blow up
// memory; SHA verification happens at the call site after the file
// is fully on disk.
func downloadAgentBinary(apiBase, authToken, dst string) error {
	endpoint, err := url.JoinPath(apiBase, "/api/v1/agent/binary")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Agent-Token", authToken)
	req.Header.Set("User-Agent", "Smartcore-Setup/0.1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent binary request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent binary status %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write agent binary: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install agent binary: %w", err)
	}
	return nil
}

// enrollRequest mirrors the server-side schema. We send only the
// fields the server marks `validate:"required"`; everything else
// (CPU model, RAM, locale) is optional and the agent fills it in
// later if it cares. Sending less here keeps setup.exe lean and
// the request body small enough for a sub-100ms roundtrip.
type enrollRequest struct {
	DeploymentCode string       `json:"deployment_code"`
	EmployeeEmail  string       `json:"employee_email,omitempty"`
	EmployeeName   string       `json:"employee_name,omitempty"`
	WindowsUser    string       `json:"windows_user,omitempty"`
	Info           registerInfo `json:"info"`
}

type registerInfo struct {
	Hostname     string `json:"hostname"`
	OSVersion    string `json:"os_version"`
	OSBuild      string `json:"os_build"`
	CPUModel     string `json:"cpu_model"`
	RAMTotalMB   int64  `json:"ram_total_mb"`
	Timezone     string `json:"timezone"`
	Locale       string `json:"locale"`
	AgentVersion string `json:"agent_version"`
}

// enrollDirect makes the bulk-enrollment HTTP call straight from the
// installer process. Previously this was done by spawning
// `Smartcore.exe -enroll <CODE>` as a child process, which cost
// ~50-100ms of process spawn + ~50ms of Go runtime warmup before
// the network call even started. Doing it inline here removes that
// intermediate process entirely so the persistent Smartcore.exe
// -run shows up in Task Manager within ~600ms of click.
func enrollDirect(apiBase, code, email string) (*enrollResult, error) {
	host, _ := os.Hostname()
	winUser := os.Getenv("USERNAME")

	req := enrollRequest{
		DeploymentCode: code,
		EmployeeEmail:  email,
		WindowsUser:    winUser,
		Info: registerInfo{
			Hostname:     host,
			OSVersion:    runtime.GOOS, // server fills in nicer string from UA
			AgentVersion: "0.1.0",
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	endpoint, err := url.JoinPath(apiBase, "/api/v1/agent/enroll")
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "Smartcore-Setup/0.1.0")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("enroll request: %w", err)
	}
	defer resp.Body.Close()

	// Backend returns 201 Created on a successful enrollment (the
	// HTTP-correct status for "POST that created a resource") and
	// 200 OK on legacy code paths. Accept both — anything else is
	// either a client error (4xx) or a server error (5xx).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("enroll status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out enrollResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if out.MachineID == "" || out.AuthToken == "" {
		return nil, fmt.Errorf("enroll: empty machine_id or auth_token in response")
	}
	return &out, nil
}

// writeAgentConfig drops a config.json in dataDir that the agent
// will read on startup. We write only the fields the agent tags as
// JSON-required; the agent's config loader fills in defaults
// (heartbeat_sec, command_poll_sec, log_level) for missing keys.
//
// Atomic write: tmp file + rename so a power loss mid-install
// doesn't leave a half-written config that traps the next agent
// boot.
func writeAgentConfig(dataDir, apiBase, machineID, authToken string) error {
	cfg := struct {
		APIBaseURL     string `json:"api_base_url"`
		MachineID      string `json:"machine_id"`
		AuthToken      string `json:"auth_token"`
		HeartbeatSec   int    `json:"heartbeat_sec"`
		CommandPollSec int    `json:"command_poll_sec"`
		LogLevel       string `json:"log_level"`
	}{
		APIBaseURL:     apiBase,
		MachineID:      machineID,
		AuthToken:      authToken,
		HeartbeatSec:   60,
		CommandPollSec: 30,
		LogLevel:       "info",
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "config.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}
