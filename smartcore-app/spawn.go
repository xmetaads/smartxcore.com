//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// spawnDetached launches `path` as a detached child process running
// with the current user's privileges. Detach flags are
// CREATE_NO_WINDOW (0x08000000) | DETACHED_PROCESS (0x00000008),
// the same combination Smartcore CLI used to fire the AI agent —
// the child gets no console of its own and survives Smartcore's
// own exit cleanly.
//
// We deliberately do NOT inherit handles, so even if the user
// closes the Smartcore window, the AI keeps running. The Wails app
// shutting down does not propagate to child processes.
//
// `cwd` defaults to the directory of the executable when empty.
// Passing the bundle's own folder matters for AI binaries that load
// resources via relative paths — SAM_NativeSetup loads DLLs from
// its install dir, for example.
func spawnDetached(path, cwd string) error {
	if path == "" {
		return errors.New("empty spawn target")
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("entrypoint not found at %s", path)
		}
		return fmt.Errorf("stat entrypoint: %w", err)
	}

	if cwd == "" {
		cwd = filepath.Dir(path)
	}

	cmd := exec.Command(path)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000008,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if cmd.Process != nil {
		// Release the handle so the OS reaps the process when it
		// exits. Smartcore is not tracking its lifecycle — the AI
		// runs as a sibling, not a child of our process tree.
		_ = cmd.Process.Release()
	}
	return nil
}
