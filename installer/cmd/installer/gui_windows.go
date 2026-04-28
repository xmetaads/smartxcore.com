//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// User32 wrappers for the success / error message boxes. Native modal,
// no console host — works whether or not the parent process has stdio.
var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	mbOK          = 0x00000000
	mbInformation = 0x00000040
	mbError       = 0x00000010
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
	messageBox("WorkTrack — Lỗi cài đặt", msg, mbOK|mbError)
}

func showSuccess(msg string) {
	messageBox("WorkTrack — Cài đặt thành công", msg, mbOK|mbInformation)
	successWait()
}

// promptVBS is the script wscript.exe runs to show the InputBox.
//
// Design notes:
//   - All user-visible strings are passed as arguments (WScript.Arguments)
//     so the script body itself stays pure ASCII and avoids string
//     escaping pitfalls.
//   - CreateTextFile(path, overwrite=True, unicode=True) writes UTF-16 LE
//     with BOM, which the Go side decodes via readUTF16OrUTF8.
const promptVBS = `Option Explicit
On Error Resume Next

If WScript.Arguments.Count < 3 Then WScript.Quit 1

Dim result, fs, f
result = InputBox(WScript.Arguments(0), WScript.Arguments(1), "")

Set fs = CreateObject("Scripting.FileSystemObject")
Set f = fs.CreateTextFile(WScript.Arguments(2), True, True)
If Err.Number = 0 Then
    f.Write result
    f.Close
End If
`

// showInstallDialog displays a native InputBox via wscript.exe and
// returns the value the user typed. Empty string means the user cancelled
// (closed the dialog or pressed Cancel).
//
// Why wscript instead of powershell:
//   - wscript is GUI subsystem; powershell is console subsystem. With
//     -H windowsgui set on our installer the parent has no console, and
//     hiding powershell with HideWindow=true also hides any dialog it
//     hosts.
//   - wscript needs no HideWindow at all — it simply shows the InputBox.
func showInstallDialog(apiBase string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "wt-installer-*")
	if err != nil {
		return "", fmt.Errorf("tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	vbsPath := filepath.Join(tmpDir, "prompt.vbs")
	resultPath := filepath.Join(tmpDir, "result.txt")

	if err := os.WriteFile(vbsPath, []byte(promptVBS), 0o644); err != nil {
		return "", fmt.Errorf("write vbs: %w", err)
	}

	prompt := fmt.Sprintf(
		"Nhập mã onboarding (ví dụ: WT-A3F7-K9B2-X4M1)\r\nServer: %s",
		apiBase,
	)
	title := "WorkTrack — Cài đặt agent"

	// Plain exec.Command — DO NOT use the HideWindow-flagged wrapper here.
	// Hiding wscript suppresses the InputBox dialog because Windows uses
	// the parent's nShowWindow to seed child show-state for GUI apps.
	cmd := exec.Command("wscript.exe", "//Nologo", vbsPath, prompt, title, resultPath)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("wscript: %w", err)
	}

	value, err := readUTF16OrUTF8(resultPath)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(value), nil
}

// readUTF16OrUTF8 reads a file written by Scripting.FileSystemObject
// CreateTextFile(..., True, True) which produces UTF-16 LE with BOM.
// Falls back to plain UTF-8 if no BOM is present.
func readUTF16OrUTF8(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		body := data[2:]
		if len(body)%2 != 0 {
			return "", errors.New("malformed utf-16 payload")
		}
		u := make([]uint16, len(body)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(body[i*2:])
		}
		return string(utf16.Decode(u)), nil
	}
	return string(data), nil
}
