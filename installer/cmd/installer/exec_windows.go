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
// agent.exe spawned during registration). DO NOT use this for wscript.exe
// when showing dialogs — see gui_windows.go for that codepath.
func newHiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

// killExistingAgent stops any running agent.exe so we can replace it.
// Best-effort: failure (no process found, lacking permissions) is silent.
// Wraps taskkill in newHiddenCommand so the brief execution leaves no
// console artifact.
func killExistingAgent() {
	cmd := newHiddenCommand("taskkill.exe", "/F", "/IM", "agent.exe")
	_ = cmd.Run()
	// Give the OS a moment to release file handles before extraction
	// overwrites the binary.
	time.Sleep(500 * time.Millisecond)
}
