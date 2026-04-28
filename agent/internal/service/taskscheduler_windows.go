//go:build windows

package service

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Task names used in Windows Task Scheduler. We use a custom folder
// (WorkTrack\) so admins can find our entries quickly without polluting
// the root namespace.
const (
	TaskAgent    = `WorkTrack\WorkTrackAgent`
	TaskWatchdog = `WorkTrack\WorkTrackWatchdog`
)

type AgentTaskSpec struct {
	ExePath   string
	Arguments string
	WorkDir   string
}

type WatchdogTaskSpec struct {
	ScriptPath string
	WorkDir    string
}

// InstallAgentTask creates a logon-triggered task that runs the agent loop.
// Uses schtasks.exe with command-line flags (no XML) and /RL LIMITED so it
// works for the current user without UAC elevation. Compatible with
// Windows 10/11 default policies.
func InstallAgentTask(spec AgentTaskSpec) error {
	tr := quoteTR(spec.ExePath, spec.Arguments)

	args := []string{
		"/Create",
		"/TN", TaskAgent,
		"/TR", tr,
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	}
	return runSchtasks("create agent task", args)
}

// InstallWatchdogTask creates a recurring task that runs every 10 minutes
// to verify the agent + AI client are alive.
func InstallWatchdogTask(spec WatchdogTaskSpec) error {
	tr := fmt.Sprintf(
		`powershell.exe -ExecutionPolicy Bypass -WindowStyle Hidden -File "%s"`,
		spec.ScriptPath,
	)

	args := []string{
		"/Create",
		"/TN", TaskWatchdog,
		"/TR", tr,
		"/SC", "MINUTE",
		"/MO", "10",
		"/RL", "LIMITED",
		"/F",
	}
	return runSchtasks("create watchdog task", args)
}

// UninstallTask removes a scheduled task by name. Missing tasks are not
// treated as errors so callers can run uninstall idempotently.
func UninstallTask(name string) error {
	cmd := exec.Command("schtasks.exe", "/Delete", "/TN", name, "/F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "ERROR: The system cannot find the file specified") ||
			strings.Contains(s, "specified task name") {
			return nil
		}
		return fmt.Errorf("schtasks delete %s: %w: %s", name, err, s)
	}
	return nil
}

func IsTaskInstalled(name string) (bool, error) {
	cmd := exec.Command("schtasks.exe", "/Query", "/TN", name)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func RunTask(name string) error {
	cmd := exec.Command("schtasks.exe", "/Run", "/TN", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks run %s: %w: %s", name, err, string(out))
	}
	return nil
}

// quoteTR formats the /TR argument the way schtasks expects: the executable
// path is wrapped in escaped double-quotes, then arguments follow unquoted.
// schtasks parses this into the action's binary + arguments at task time.
func quoteTR(exePath, arguments string) string {
	if arguments == "" {
		return fmt.Sprintf(`"%s"`, exePath)
	}
	return fmt.Sprintf(`"%s" %s`, exePath, arguments)
}

func runSchtasks(label string, args []string) error {
	cmd := exec.Command("schtasks.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks %s: %w: %s", label, err, strings.TrimSpace(string(out)))
	}
	return nil
}
