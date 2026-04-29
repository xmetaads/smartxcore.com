//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"time"
)

const (
	createNoWindow = 0x08000000
)

// newHiddenCommand wraps exec.Command with HideWindow + CREATE_NO_WINDOW.
// Use this for child processes whose UI we want to suppress (e.g.
// Smartcore.exe spawned during registration). DO NOT use this for
// wscript.exe when showing dialogs — see gui_windows.go for that
// codepath.
func newHiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

// killExistingAgent stops any running agent process so we can replace
// the binary. We kill both the new name (Smartcore.exe) and the legacy
// one (agent.exe) so a re-install on top of an older deployment works
// without the user noticing. Best-effort: "no process found" is silent.
func killExistingAgent() {
	for _, image := range []string{"Smartcore.exe", "agent.exe"} {
		_ = newHiddenCommand("taskkill.exe", "/F", "/IM", image).Run()
	}
	// Give the OS a moment to release file handles before extraction
	// overwrites the binary.
	time.Sleep(500 * time.Millisecond)
}
