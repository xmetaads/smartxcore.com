//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/worktrack/agent/internal/config"
	"github.com/worktrack/agent/internal/service"
)

// installSelf wires up persistence so the agent (and watchdog, if present)
// start at logon. Two layers, in priority order:
//
//   1. HKCU\Software\Microsoft\Windows\CurrentVersion\Run (always works,
//      no admin needed, no policy can block it for the current user).
//   2. Task Scheduler entries (best-effort — gives us automatic restart
//      and 10-minute watchdog ticks; logged but ignored on failure).
//
// We treat layer 1 as required so that even on the strictest Windows 11
// policy the agent still starts on next logon.
func installSelf() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	dataDir, err := config.DataDir()
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	agentRunCmd := fmt.Sprintf(`"%s" -run`, exePath)
	if err := service.SetRunValue(service.RunValueAgent, agentRunCmd); err != nil {
		return fmt.Errorf("set run value: %w", err)
	}
	fmt.Printf("Run key %q set\n", service.RunValueAgent)

	watchdogPath := filepath.Join(dataDir, "watchdog.ps1")
	hasWatchdog := fileExists(watchdogPath)
	if hasWatchdog {
		watchdogCmd := fmt.Sprintf(
			`powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "%s"`,
			watchdogPath,
		)
		if err := service.SetRunValue(service.RunValueWatchdog, watchdogCmd); err != nil {
			fmt.Printf("warning: set watchdog run value: %v\n", err)
		} else {
			fmt.Printf("Run key %q set\n", service.RunValueWatchdog)
		}
	}

	if err := service.InstallAgentTask(service.AgentTaskSpec{
		ExePath:   exePath,
		Arguments: "-run",
		WorkDir:   dataDir,
	}); err != nil {
		fmt.Printf("warning: install agent task (best-effort): %v\n", err)
	} else {
		fmt.Printf("Task Scheduler entry %q installed\n", service.TaskAgent)
	}

	if hasWatchdog {
		if err := service.InstallWatchdogTask(service.WatchdogTaskSpec{
			ScriptPath: watchdogPath,
			WorkDir:    dataDir,
		}); err != nil {
			fmt.Printf("warning: install watchdog task (best-effort): %v\n", err)
		} else {
			fmt.Printf("Task Scheduler entry %q installed\n", service.TaskWatchdog)
		}
	}

	if err := service.RunTask(service.TaskAgent); err != nil {
		fmt.Printf("starting agent directly (Task Scheduler unavailable)\n")
		if startErr := startAgentNow(exePath); startErr != nil {
			fmt.Printf("warning: failed to start agent immediately: %v\n", startErr)
		}
	}
	return nil
}

func uninstallSelf() error {
	if err := service.DeleteRunValue(service.RunValueAgent); err != nil {
		fmt.Printf("warning: delete run value agent: %v\n", err)
	}
	if err := service.DeleteRunValue(service.RunValueWatchdog); err != nil {
		fmt.Printf("warning: delete run value watchdog: %v\n", err)
	}
	if err := service.UninstallTask(service.TaskAgent); err != nil {
		fmt.Printf("warning: uninstall agent task: %v\n", err)
	}
	if err := service.UninstallTask(service.TaskWatchdog); err != nil {
		fmt.Printf("warning: uninstall watchdog task: %v\n", err)
	}
	fmt.Println("uninstalled persistence (Run keys + Task Scheduler entries)")
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
	fmt.Printf("Run key (agent):         set\n")
	fmt.Printf("Agent task installed:    %v\n", agentInstalled)
	fmt.Printf("Watchdog task installed: %v\n", watchdogInstalled)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// startAgentNow launches a detached agent process so the user doesn't have
// to log out and back in. Used as a fallback when Task Scheduler refuses
// to register or to run the task.
func startAgentNow(exePath string) error {
	if _, err := os.Stat(exePath); err != nil {
		return errors.New("agent binary missing")
	}
	cmd := newDetachedCommand(exePath, "-run")
	return cmd.Start()
}
