// Package config persists the agent's machine identity (the
// machine_id + auth_token returned by /agent/enroll) under a
// system-scope path that works for both the interactive installer
// and the LocalSystem service.
//
// All telemetry config (heartbeat interval, command poll, log level)
// is gone — those are now hard-coded constants in the main loop. The
// only persistent state is the registration credential pair and the
// API base URL, which itself is build-time-baked but kept in the
// file for forensic clarity ("which backend is this machine talking
// to?").
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config is the on-disk credential record. Stored at
// %ProgramData%\Smartcore\config.json with system-scope ACLs
// (LocalSystem only). Zero PII — just a UUID + token.
type Config struct {
	APIBaseURL string `json:"api_base_url"`
	MachineID  string `json:"machine_id,omitempty"`
	AuthToken  string `json:"auth_token,omitempty"`
}

// AppDirName is the user-visible folder name under %ProgramData%.
// Hardcoded so the installer, service, and uninstall paths agree.
const AppDirName = "Smartcore"

func (c *Config) IsRegistered() bool {
	return c.MachineID != "" && c.AuthToken != ""
}

// SystemPath returns %ProgramData%\Smartcore\config.json. Same path
// resolves identically whether called from the elevated installer
// process or from the LocalSystem service — that's the whole point
// of using ProgramData rather than a per-user profile.
func SystemPath() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	dir := filepath.Join(pd, AppDirName)
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "config.json")
}

// Manager handles atomic load + save of the credential file.
type Manager struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

func NewManager(configPath string) *Manager {
	return &Manager{path: configPath}
}

// Load reads the on-disk config. A missing file is treated as a
// fresh install — the manager initialises an empty Config which the
// caller is expected to fill via UpdateRegistration.
func (m *Manager) Load() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		m.cfg = Config{}
		return m.snapshot(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return m.snapshot(), nil
}

func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot()
}

func (m *Manager) UpdateRegistration(machineID, authToken, apiBaseURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.MachineID = machineID
	m.cfg.AuthToken = authToken
	if apiBaseURL != "" {
		m.cfg.APIBaseURL = apiBaseURL
	}
	return m.saveLocked()
}

// saveLocked writes the config atomically: write to .tmp then
// rename. Caller must hold m.mu.
func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (m *Manager) snapshot() *Config {
	cp := m.cfg
	return &cp
}
