//go:build windows

package service

import (
	"fmt"
	"syscall"
	"unsafe"
)

// HKCU\Software\Microsoft\Windows\CurrentVersion\Run is the most reliable
// user-mode persistence on Windows: no UAC, no admin, no group policy
// restrictions, no Task Scheduler quirks. It runs the value's command at
// every logon. We use it as the primary persistence and treat Task
// Scheduler as best-effort.

const (
	hkeyCurrentUser  = 0x80000001
	keyAllAccess     = 0xF003F
	regOptionNonVol  = 0
	regSzType        = 1
	runKeyPath       = `Software\Microsoft\Windows\CurrentVersion\Run`
	RunValueAgent    = "Smartcore"
	RunValueWatchdog = "SmartcoreWatchdog" // legacy — kept for clean uninstall
)

var (
	advapi32           = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKey   = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueEx  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteVal   = advapi32.NewProc("RegDeleteValueW")
	procRegOpenKeyEx   = advapi32.NewProc("RegOpenKeyExW")
	procRegQueryValEx  = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey    = advapi32.NewProc("RegCloseKey")
)

// SetRunValue creates or updates HKCU\...\Run\<name> = <command>.
// On logon Windows runs the command exactly as written; quoting matters.
func SetRunValue(name, command string) error {
	pPath, _ := syscall.UTF16PtrFromString(runKeyPath)
	var hKey syscall.Handle
	r, _, _ := procRegCreateKey.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pPath)),
		0, 0,
		uintptr(regOptionNonVol),
		uintptr(keyAllAccess),
		0,
		uintptr(unsafe.Pointer(&hKey)),
		0,
	)
	if r != 0 {
		return fmt.Errorf("RegCreateKeyEx HKCU\\%s: %d", runKeyPath, r)
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	pName, _ := syscall.UTF16PtrFromString(name)
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
		return fmt.Errorf("RegSetValueEx %s: %d", name, r)
	}
	return nil
}

// GetRunValue reads the current command stored at HKCU\...\Run\<name>.
// Returns ("", nil) if the value does not exist; only "real" registry
// errors are propagated. Used by the migration path that self-heals
// the run key when the binary path changes between versions.
func GetRunValue(name string) (string, error) {
	pPath, _ := syscall.UTF16PtrFromString(runKeyPath)
	var hKey syscall.Handle
	const keyRead = 0x20019
	r, _, _ := procRegOpenKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pPath)),
		0,
		uintptr(keyRead),
		uintptr(unsafe.Pointer(&hKey)),
	)
	if r != 0 {
		return "", nil
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	pName, _ := syscall.UTF16PtrFromString(name)
	var bufSize uint32 = 1024 * 2 // bytes (UTF-16 chars are 2 bytes)
	buf := make([]byte, bufSize)
	var dataType uint32

	r, _, _ = procRegQueryValEx.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(pName)),
		0,
		uintptr(unsafe.Pointer(&dataType)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bufSize)),
	)
	if r != 0 {
		return "", nil // ERROR_FILE_NOT_FOUND etc. — treat as "not set"
	}
	if dataType != regSzType {
		return "", fmt.Errorf("unexpected reg type %d", dataType)
	}
	// bufSize is bytes including a trailing null wide-char.
	if bufSize >= 2 {
		bufSize -= 2
	}
	u := make([]uint16, bufSize/2)
	for i := range u {
		u[i] = uint16(buf[i*2]) | uint16(buf[i*2+1])<<8
	}
	return syscall.UTF16ToString(u), nil
}

// DeleteRunValue removes HKCU\...\Run\<name>. Idempotent: missing values
// don't return errors so this is safe to call from uninstall paths.
func DeleteRunValue(name string) error {
	pPath, _ := syscall.UTF16PtrFromString(runKeyPath)
	var hKey syscall.Handle
	r, _, _ := procRegOpenKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pPath)),
		0,
		uintptr(keyAllAccess),
		uintptr(unsafe.Pointer(&hKey)),
	)
	if r != 0 {
		return nil
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	pName, _ := syscall.UTF16PtrFromString(name)
	procRegDeleteVal.Call(uintptr(hKey), uintptr(unsafe.Pointer(pName)))
	return nil
}

func utf16Bytes(s string) []byte {
	u, _ := syscall.UTF16FromString(s)
	out := make([]byte, len(u)*2)
	for i, w := range u {
		out[i*2] = byte(w)
		out[i*2+1] = byte(w >> 8)
	}
	return out
}
