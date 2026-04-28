//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const (
	createNoWindow = 0x08000000
)

// newCommand wraps exec.Command with HideWindow + CREATE_NO_WINDOW so the
// PowerShell helper used to render the input dialog does not flash a black
// console behind the GUI.
func newCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
