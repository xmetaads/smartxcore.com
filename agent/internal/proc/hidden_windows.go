//go:build windows

package proc

import (
	"context"
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW + HideWindow ensures Go's child processes never
// flash a console window. Without these flags any console-subsystem
// child briefly shows a black box even when stdin/stdout are
// redirected — visible to the user, and a bad look on a fleet of
// 2000 employee machines. With them, the spawn is invisible.
const (
	createNoWindow    = 0x08000000
	detachedProcess   = 0x00000008
	createNewConsole  = 0x00000010
)

// Command returns an exec.Cmd configured to run with no visible window.
// Use this everywhere instead of exec.Command on Windows for child
// processes the user shouldn't see.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = hiddenSysProcAttr()
	return cmd
}

// CommandContext is the context-aware variant.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = hiddenSysProcAttr()
	return cmd
}

// Detached configures a fully detached child that survives parent exit
// and shows no window. Used to spawn the agent loop from the installer.
func Detached(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNoWindow,
	}
	return cmd
}

func hiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
