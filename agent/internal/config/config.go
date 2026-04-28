package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config is the persistent agent configuration written to disk.
// Stored at %LOCALAPPDATA%\WorkTrack\config.json with restrictive ACLs.
type Config struct {
	APIBaseURL    string `json:"api_base_url"`
	MachineID     string `json:"machine_id,omitempty"`
	AuthToken     string `json:"auth_token,omitempty"`
	AgentVersion  string `json:"-"`
	HeartbeatSec  int    `json:"heartbeat_sec"`
	CommandPollSec int   `json:"command_poll_sec"`
	LogLevel      string `json:"log_level"`
}

// Default values applied when fields are missing in the config file.
func (c *Config) applyDefaults() {
	if c.APIBaseURL == "" {
		c.APIBaseURL = "https://api.example.com"
	}
	if c.HeartbeatSec <= 0 {
		c.HeartbeatSec = 60
	}
	if c.CommandPollSec <= 0 {
		c.CommandPollSec = 30
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
}

func (c *Config) Validate() error {
	if c.APIBaseURL == "" {
		return errors.New("api_base_url is required")
	}
	return nil
}

func (c *Config) IsRegistered() bool {
	return c.MachineID != "" && c.AuthToken != ""
}

// Manager handles loading, saving, and atomic updates of the config file.
type Manager struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

func NewManager(configPath string) *Manager {
	return &Manager{path: configPath}
}

// DefaultPath returns the standard config location for the current user.
func DefaultPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// DataDir returns %LOCALAPPDATA%\WorkTrack on Windows.
func DataDir() (string, error) {
	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		appData = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(appData, "WorkTrack")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	return dir, nil
}

func (m *Manager) Load() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		m.cfg = Config{}
		m.cfg.applyDefaults()
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
		return m.snapshot(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	m.cfg.applyDefaults()
	return m.snapshot(), nil
}

func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot()
}

func (m *Manager) UpdateRegistration(machineID, authToken string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.MachineID = machineID
	m.cfg.AuthToken = authToken
	return m.saveLocked()
}

// saveLocked writes config atomically: write to .tmp then rename.
// Caller must hold m.mu.
func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp config: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func (m *Manager) snapshot() *Config {
	cp := m.cfg
	return &cp
}
