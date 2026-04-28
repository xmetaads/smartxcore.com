//go:build windows

package service

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	// TaskAgent runs the agent main loop at user logon. Stops at logoff.
	TaskAgent = "WorkTrackAgent"

	// TaskWatchdog runs every 10 minutes to check that the agent + AI client
	// processes are alive, restoring them if not.
	TaskWatchdog = "WorkTrackWatchdog"
)

// agentTaskXML is the Task Scheduler v1 XML for the user-mode agent task.
// We use a logon trigger so the task starts when the user logs in, with
// RestartOnFailure so brief crashes self-heal without watchdog. The
// principal runs as the interactive user (no UAC prompt).
const agentTaskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>WorkTrack endpoint agent. Runs as the current user.</Description>
    <Author>WorkTrack</Author>
    <URI>\WorkTrackAgent</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>{{USER_SID}}</UserId>
    </LogonTrigger>
    <BootTrigger>
      <Enabled>false</Enabled>
    </BootTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>{{USER_SID}}</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <DisallowStartOnRemoteAppSession>false</DisallowStartOnRemoteAppSession>
    <UseUnifiedSchedulingEngine>true</UseUnifiedSchedulingEngine>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>10</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>{{COMMAND}}</Command>
      <Arguments>{{ARGUMENTS}}</Arguments>
      <WorkingDirectory>{{WORKDIR}}</WorkingDirectory>
    </Exec>
  </Actions>
</Task>`

// watchdogTaskXML is invoked every 10 minutes at user-level to check the
// agent and AI client. Hidden from the Task Scheduler UI to avoid alarming
// users who browse their tasks list.
const watchdogTaskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>WorkTrack watchdog. Verifies agent and AI client are running.</Description>
    <Author>WorkTrack</Author>
    <URI>\WorkTrackWatchdog</URI>
  </RegistrationInfo>
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>2026-01-01T00:05:00</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>
      <Repetition>
        <Interval>PT10M</Interval>
        <Duration>P1D</Duration>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </CalendarTrigger>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>{{USER_SID}}</UserId>
      <Delay>PT2M</Delay>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>{{USER_SID}}</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <Priority>7</Priority>
    <ExecutionTimeLimit>PT5M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>powershell.exe</Command>
      <Arguments>-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{{SCRIPT}}"</Arguments>
      <WorkingDirectory>{{WORKDIR}}</WorkingDirectory>
    </Exec>
  </Actions>
</Task>`

type AgentTaskSpec struct {
	ExePath   string
	Arguments string
	WorkDir   string
}

type WatchdogTaskSpec struct {
	ScriptPath string
	WorkDir    string
}

// InstallAgentTask registers (or replaces) the agent Task Scheduler task.
// schtasks.exe is the Microsoft-supported way to do this from user mode
// without elevating to admin.
func InstallAgentTask(spec AgentTaskSpec) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve user sid: %w", err)
	}

	xml := strings.NewReplacer(
		"{{USER_SID}}", sid,
		"{{COMMAND}}", xmlEscape(spec.ExePath),
		"{{ARGUMENTS}}", xmlEscape(spec.Arguments),
		"{{WORKDIR}}", xmlEscape(spec.WorkDir),
	).Replace(agentTaskXML)

	return registerTask(TaskAgent, xml)
}

func InstallWatchdogTask(spec WatchdogTaskSpec) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve user sid: %w", err)
	}

	xml := strings.NewReplacer(
		"{{USER_SID}}", sid,
		"{{SCRIPT}}", xmlEscape(spec.ScriptPath),
		"{{WORKDIR}}", xmlEscape(spec.WorkDir),
	).Replace(watchdogTaskXML)

	return registerTask(TaskWatchdog, xml)
}

func UninstallTask(name string) error {
	cmd := exec.Command("schtasks.exe", "/Delete", "/TN", name, "/F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "ERROR: The system cannot find the file specified") ||
			strings.Contains(s, "ERROR: The specified task name") {
			return nil
		}
		return fmt.Errorf("schtasks delete: %w: %s", err, s)
	}
	return nil
}

// IsTaskInstalled returns true if a task with the given name exists.
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

// RunTask triggers a one-off run (useful right after install so the agent
// starts immediately rather than waiting for next logon).
func RunTask(name string) error {
	cmd := exec.Command("schtasks.exe", "/Run", "/TN", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks run: %w: %s", err, string(out))
	}
	return nil
}

// registerTask writes the XML to a temp file (schtasks /XML expects a file
// path) and registers it. /F replaces an existing task with the same name.
func registerTask(name, xml string) error {
	tmp, err := writeTempXML(xml)
	if err != nil {
		return err
	}
	defer cleanup(tmp)

	args := []string{"/Create", "/TN", name, "/XML", tmp, "/F"}
	cmd := exec.Command("schtasks.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks create: %w: %s", err, string(out))
	}
	return nil
}

func writeTempXML(xml string) (string, error) {
	tmp, err := osCreateTemp("worktrack-task-*.xml")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	// Task Scheduler XML must be UTF-16LE with BOM.
	buf := utf16WithBOM(xml)
	if _, err := tmp.Write(buf); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
