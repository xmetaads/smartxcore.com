//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// User32 wrappers for the success / error message boxes. Native modal,
// no console host — works whether or not the parent process has stdio.
//
// Earlier revisions of this file also hosted a wscript-backed
// InputBox path used to prompt the employee for a deployment code or
// onboarding code. Both paths are gone: the deployment code is baked
// into the binary at build time (see main.deploymentCode in main.go)
// and the splash UI runs the install without any text input. Keeping
// the wscript launch around left the literal string "wscript.exe" in
// the binary, which malware-heuristic scanners flag because that
// process name is on every LOLBAS list. Removing it brings setup.exe
// into the same forbidden-strings = 0 state Smartcore.exe maintains.
var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	mbOK    = 0x00000000
	mbError = 0x00000010
)

func messageBox(title, body string, flags uintptr) {
	t, _ := syscall.UTF16PtrFromString(title)
	b, _ := syscall.UTF16PtrFromString(body)
	procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(b)),
		uintptr(unsafe.Pointer(t)),
		flags,
	)
}

func showError(msg string) {
	messageBox("Smartcore — Setup failed", msg, mbOK|mbError)
}
