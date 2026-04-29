//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// HKCU\Software\Microsoft\Windows\CurrentVersion\Run is the standard
// user-mode autostart mechanism on Windows. Same code path Discord,
// Slack, Steam, OneDrive use. No UAC, no admin, no Task Scheduler.
//
// We write the key directly from setup.exe instead of letting
// `Smartcore.exe -enroll` do it: that saves the full process-spawn
// roundtrip and lets the persistent Smartcore.exe -run start a
// second earlier.

const (
	hkeyCurrentUser = 0x80000001
	keyAllAccess    = 0xF003F
	regSzType       = 1
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`

	runValueAgent = "Smartcore"
)

// setRunValue creates or overwrites HKCU\...\Run\<runValueAgent> with
// the given command. The command should already be quoted if it
// contains spaces — Windows runs the value verbatim at logon.
func setRunValue(command string) error {
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKey := advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueEx := advapi32.NewProc("RegSetValueExW")
	procRegCloseKey := advapi32.NewProc("RegCloseKey")

	pPath, _ := syscall.UTF16PtrFromString(runKeyPath)
	var hKey syscall.Handle
	r, _, _ := procRegCreateKey.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pPath)),
		0, 0,
		uintptr(0), // REG_OPTION_NON_VOLATILE
		uintptr(keyAllAccess),
		0,
		uintptr(unsafe.Pointer(&hKey)),
		0,
	)
	if r != 0 {
		return fmt.Errorf("RegCreateKeyEx HKCU\\%s: %d", runKeyPath, r)
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	pName, _ := syscall.UTF16PtrFromString(runValueAgent)
	pData := utf16Bytes(command)

	r, _, _ = procRegSetValueEx.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(pName)),
		0,
		uintptr(regSzType),
		uintptr(unsafe.Pointer(&pData[0])),
		uintptr(len(pData)),
	)
	if r != 0 {
		return fmt.Errorf("RegSetValueEx %s: %d", runValueAgent, r)
	}
	return nil
}

// utf16Bytes returns the little-endian UTF-16 encoding of s as a
// byte slice including the trailing null wide-char that REG_SZ
// values must carry.
func utf16Bytes(s string) []byte {
	u, _ := syscall.UTF16FromString(s)
	out := make([]byte, len(u)*2)
	for i, w := range u {
		out[i*2] = byte(w)
		out[i*2+1] = byte(w >> 8)
	}
	return out
}
