//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
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

// showInstallDialog shows the onboarding-code input prompt.
//
// Implementation: write a tiny VBScript that calls InputBox, then run it
// via wscript.exe. wscript is the Windows GUI host — unlike powershell it
// has no console, so the prompt appears as a clean modal with zero
// console flash regardless of HideWindow flags. The result is written to
// a temp UTF-16 file because wscript has no stdout we can capture.
func showInstallDialog(apiBase string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "wt-installer-*")
	if err != nil {
		return "", fmt.Errorf("tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	vbsPath := filepath.Join(tmpDir, "prompt.vbs")
	resultPath := filepath.Join(tmpDir, "result.txt")

	vbs := buildPromptVBS(apiBase, resultPath)
	if err := os.WriteFile(vbsPath, []byte(vbs), 0o644); err != nil {
		return "", fmt.Errorf("write vbs: %w", err)
	}

	cmd := newCommand("wscript.exe", "//B", "//Nologo", vbsPath)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("wscript: %w", err)
	}

	value, err := readUTF16OrUTF8(resultPath)
	if err != nil {
		// File may not exist if the user closed the dialog without OK.
		// Treat as cancel (empty input) — the caller decides.
		return "", nil
	}
	return strings.TrimSpace(value), nil
}

func buildPromptVBS(apiBase, resultPath string) string {
	prompt := strings.ReplaceAll(
		fmt.Sprintf("Nhập mã onboarding (ví dụ: WT-A3F7-K9B2-X4M1)%sServer: %s", "\" & vbCrLf & \"", apiBase),
		"\n", " ",
	)
	prompt = escapeVBSString(prompt)
	title := escapeVBSString("WorkTrack — Cài đặt agent")
	out := escapeVBSString(resultPath)

	return fmt.Sprintf(`Option Explicit
On Error Resume Next
Dim result, fs, f
result = InputBox(%q, %q, "")
If IsNull(result) Then result = ""
Set fs = CreateObject("Scripting.FileSystemObject")
Set f = fs.CreateTextFile(%q, True, True)
If Err.Number = 0 Then
    f.Write result
    f.Close
End If
`, prompt, title, out)
}

// escapeVBSString escapes embedded double-quotes for VBS string literals.
// The Go %q format emits Go-style strings; VBS uses doubled quotes
// (e.g. "He said ""hi""") so we adjust after Sprintf if needed. Here we
// just escape any literal " present in the input; the surrounding %q is
// done by the caller.
func escapeVBSString(s string) string {
	return strings.ReplaceAll(s, `"`, `""`)
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
