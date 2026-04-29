//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
)

const (
	createNoWindow  = 0x08000000
	detachedProcess = 0x00000008
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

// spawnDetached starts a child process that survives our exit. Used
// to launch Smartcore.exe -run at the end of install: the agent has
// to keep running after setup.exe closes its splash and exits, and
// we don't want to track its lifecycle.
//
// Flags:
//   - createNoWindow:  child has no console window.
//   - detachedProcess: child has no parent console; if setup.exe
//     terminates, the child stays alive cleanly.
//
// We Release() the process handle so the OS reaps the child when it
// eventually exits without going through us.
func spawnDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = filepath.Dir(name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | detachedProcess,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}

// killExistingAgent stops any running agent process so we can replace
// the binary. We kill both the new name (Smartcore.exe) and the legacy
// one (agent.exe) in parallel — they're independent subprocess execs
// and serial-running them was wasting ~50ms × 2.
//
// We no longer sleep after killing. The OS releases file handles
// synchronously when TerminateProcess returns; the retry-with-backoff
// inside writeEmbedded covers the rare case where the rename hits a
// briefly-still-locked binary, costing nothing on the 99% path.
func killExistingAgent() {
	var wg sync.WaitGroup
	for _, image := range []string{"Smartcore.exe", "agent.exe"} {
		wg.Add(1)
		go func(img string) {
			defer wg.Done()
			_ = newHiddenCommand("taskkill.exe", "/F", "/IM", img).Run()
		}(image)
	}
	wg.Wait()
}
