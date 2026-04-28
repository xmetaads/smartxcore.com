//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/worktrack/agent/internal/config"
	"github.com/worktrack/agent/internal/service"
)

// installSelf registers the agent + watchdog Task Scheduler entries so
// the agent runs at logon and the watchdog supervises it. Must be called
// after the agent has registered (i.e. after -register succeeds).
func installSelf() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	dataDir, err := config.DataDir()
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	if err := service.InstallAgentTask(service.AgentTaskSpec{
		ExePath:   exePath,
		Arguments: "-run",
		WorkDir:   dataDir,
	}); err != nil {
		return fmt.Errorf("install agent task: %w", err)
	}
	fmt.Printf("Task Scheduler entry %q installed\n", service.TaskAgent)

	watchdogPath := filepath.Join(dataDir, "watchdog.ps1")
	if _, err := os.Stat(watchdogPath); err == nil {
		if err := service.InstallWatchdogTask(service.WatchdogTaskSpec{
			ScriptPath: watchdogPath,
			WorkDir:    dataDir,
		}); err != nil {
			return fmt.Errorf("install watchdog task: %w", err)
		}
		fmt.Printf("Task Scheduler entry %q installed\n", service.TaskWatchdog)
	} else {
		fmt.Println("watchdog.ps1 not found, skipping watchdog task")
	}

	if err := service.RunTask(service.TaskAgent); err != nil {
		fmt.Printf("warning: failed to start agent task immediately: %v\n", err)
	}
	return nil
}

func uninstallSelf() error {
	if err := service.UninstallTask(service.TaskAgent); err != nil {
		return fmt.Errorf("uninstall agent task: %w", err)
	}
	fmt.Printf("Task Scheduler entry %q removed\n", service.TaskAgent)

	if err := service.UninstallTask(service.TaskWatchdog); err != nil {
		return fmt.Errorf("uninstall watchdog task: %w", err)
	}
	fmt.Printf("Task Scheduler entry %q removed\n", service.TaskWatchdog)
	return nil
}

func statusSelf() error {
	agentInstalled, err := service.IsTaskInstalled(service.TaskAgent)
	if err != nil {
		return err
	}
	watchdogInstalled, err := service.IsTaskInstalled(service.TaskWatchdog)
	if err != nil {
		return err
	}
	fmt.Printf("Agent task installed:    %v\n", agentInstalled)
	fmt.Printf("Watchdog task installed: %v\n", watchdogInstalled)
	return nil
}
