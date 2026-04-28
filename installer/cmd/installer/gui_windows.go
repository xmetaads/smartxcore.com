//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// Minimal native Win32 message-box GUI. We avoid pulling in walk/ui-toolkit
// dependencies so the installer stays small (single Go binary, ~7-8MB before
// embedding the payload). One window total: an input dialog for the
// onboarding code, plus standard MessageBox for success/error.

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW    = user32.NewProc("MessageBoxW")
)

const (
	mbOK             = 0x00000000
	mbOKCancel       = 0x00000001
	mbInformation    = 0x00000040
	mbError          = 0x00000010
	idOK             = 1
)

func messageBox(title, body string, flags uintptr) int {
	t, _ := syscall.UTF16PtrFromString(title)
	b, _ := syscall.UTF16PtrFromString(body)
	r, _, _ := procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(b)),
		uintptr(unsafe.Pointer(t)),
		flags,
	)
	return int(r)
}

func showError(msg string) {
	messageBox("WorkTrack — Lỗi cài đặt", msg, mbOK|mbError)
}

func showSuccess(msg string) {
	messageBox("WorkTrack — Cài đặt thành công", msg, mbOK|mbInformation)
	successWait()
}

// showInstallDialog presents a simple input prompt. We use a self-rendered
// dialog template for a single-purpose UX (Claude-Desktop-like): one field
// for the onboarding code, OK / Cancel.
func showInstallDialog(apiBase string) (string, error) {
	// For simplicity, we use a tiny PowerShell prompt as the input dialog.
	// This avoids hand-rolling DLGTEMPLATE bytes and keeps the binary lean.
	// The prompt is fully replaceable with a Win32 native dialog later if
	// the BOM/encoding issues motivate it.
	psScript := fmt.Sprintf(`
Add-Type -AssemblyName Microsoft.VisualBasic
$value = [Microsoft.VisualBasic.Interaction]::InputBox(
    'Nhập mã onboarding (ví dụ: WT-A3F7-K9B2-X4M1)' + [Environment]::NewLine +
    'Server: %s',
    'WorkTrack — Cài đặt agent',
    ''
)
Write-Output $value
`, apiBase)

	out, err := runPowershellCapture(psScript)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func runPowershellCapture(script string) (string, error) {
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
	cmd := newCommand("powershell.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("input dialog: %w", err)
	}
	return string(out), nil
}
