//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// newDetachedCommand returns an exec.Cmd configured to run hidden and
// disconnect from the parent so the agent keeps running after the
// installer exits.
func newDetachedCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x08000000, // DETACHED_PROCESS | CREATE_NO_WINDOW
	}
	return cmd
}
