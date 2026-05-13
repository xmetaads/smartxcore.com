//go:build windows

package main

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// win32.go - small native Win32 helpers used by the UI layer.
//
// We deliberately keep this file tiny and import-light: the goal
// is to ship a bootstrapper whose binary surface looks like every
// other small Windows installer, not like a heavyweight framework
// app. The only DLLs we touch are user32 (MessageBox) and
// shell32 (ShellExecute, in install_windows.go).

// getStderr returns the standard error stream, falling back to
// io.Discard if the process was launched without a console
// (the bootstrapper compiles as -H windowsgui by default, which
// detaches stdio). The wrapper means our code never crashes when
// trying to Fprintln to nil.
func getStderr() io.Writer {
	if os.Stderr == nil {
		return io.Discard
	}
	return os.Stderr
}

// showMessageBox calls user32!MessageBoxW. uType is a Win32
// MB_* bit mask (e.g. MB_ICONERROR | MB_OK).
//
// Lazy-loads user32; this is the single MessageBox the
// bootstrapper shows (on error), so paying the load cost once is
// fine and the symbol doesn't end up in the static import table
// (which would bloat .idata and surface the call in tools that
// scan imports).
func showMessageBox(title, body string, uType uint32) int {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	bodyPtr, _ := syscall.UTF16PtrFromString(body)

	user32 := syscall.NewLazyDLL("user32.dll")
	mb := user32.NewProc("MessageBoxW")
	r, _, _ := mb.Call(
		0, // hwnd (no parent)
		uintptr(unsafe.Pointer(bodyPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(uType),
	)
	return int(r)
}
