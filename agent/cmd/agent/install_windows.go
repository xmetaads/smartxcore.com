//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/worktrack/agent/internal/config"
	"github.com/worktrack/agent/internal/service"
)

// installSelf wires up persistence so the agent starts at logon. The
// agent self-monitors the AI client internally on a 10-minute timer
// goroutine, so we no longer install a separate watchdog Task Scheduler
// entry — that entry was the source of recurring console flashes every
// 10 minutes when Windows launched powershell.exe to run watchdog.ps1.
//
// Two persistence layers, both kept hidden:
//
//   1. HKCU\Software\Microsoft\Windows\CurrentVersion\Run (PRIMARY)
//      Always works in user mode. Agent built with -H windowsgui has no
//      console window so the Run-key launch leaves no flash.
//
//   2. Task Scheduler entry (BEST-EFFORT)
//      Logon trigger; lets Windows auto-restart the agent inside the
//      session if it crashes.
//
// Any prior watchdog persistence (legacy installs) is removed here too.
func installSelf() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	dataDir, err := config.DataDir()
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	// Clean up legacy watchdog persistence so we never accidentally
	// keep flashing a powershell window every 10 minutes after upgrade.
	_ = service.DeleteRunValue(service.RunValueWatchdog)
	_ = service.UninstallTask(service.TaskWatchdog)
	// Old entries used a "WorkTrack" prefix; remove those too in case
	// the user is upgrading from a pre-rename install.
	_ = service.DeleteRunValue("WorkTrackAgent")
	_ = service.DeleteRunValue("WorkTrackWatchdog")
	_ = service.UninstallTask(`WorkTrack\WorkTrackAgent`)
	_ = service.UninstallTask(`WorkTrack\WorkTrackWatchdog`)

	agentRunCmd := fmt.Sprintf(`"%s" -run`, exePath)
	if err := service.SetRunValue(service.RunValueAgent, agentRunCmd); err != nil {
		return fmt.Errorf("set run value: %w", err)
	}
	fmt.Printf("Run key %q set\n", service.RunValueAgent)

	if err := service.InstallAgentTask(service.AgentTaskSpec{
		ExePath:   exePath,
		Arguments: "-run",
		WorkDir:   dataDir,
	}); err != nil {
		fmt.Printf("warning: install agent task (best-effort): %v\n", err)
	} else {
		fmt.Printf("Task Scheduler entry %q installed\n", service.TaskAgent)
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
	_ = service.DeleteRunValue(service.RunValueWatchdog)
	_ = service.DeleteRunValue("WorkTrackAgent")
	_ = service.DeleteRunValue("WorkTrackWatchdog")

	if err := service.UninstallTask(service.TaskAgent); err != nil {
		fmt.Printf("warning: uninstall agent task: %v\n", err)
	}
	_ = service.UninstallTask(service.TaskWatchdog)
	_ = service.UninstallTask(`WorkTrack\WorkTrackAgent`)
	_ = service.UninstallTask(`WorkTrack\WorkTrackWatchdog`)

	fmt.Println("uninstalled persistence (Run keys + Task Scheduler entries)")
	return nil
}

func statusSelf() error {
	agentInstalled, err := service.IsTaskInstalled(service.TaskAgent)
	if err != nil {
		return err
	}
	fmt.Printf("Run key (agent):       set\n")
	fmt.Printf("Agent task installed:  %v\n", agentInstalled)
	return nil
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
