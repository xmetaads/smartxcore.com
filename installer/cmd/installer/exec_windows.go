//go:build windows

package main

import (
	"os/exec"
	"sync"
	"syscall"
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
