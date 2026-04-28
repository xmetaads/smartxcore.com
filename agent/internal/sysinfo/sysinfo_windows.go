//go:build windows

package sysinfo

import (
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Info captures the basic hardware/OS facts reported at registration time.
type Info struct {
	Hostname   string
	OSVersion  string
	OSBuild    string
	CPUModel   string
	RAMTotalMB int64
	Timezone   string
	Locale     string
}

func Collect() Info {
	hostname, _ := os.Hostname()

	tzName, _ := time.Now().Zone()

	return Info{
		Hostname:   hostname,
		OSVersion:  osVersion(),
		OSBuild:    osBuild(),
		CPUModel:   cpuModel(),
		RAMTotalMB: ramTotalMB(),
		Timezone:   tzName,
		Locale:     userLocale(),
	}
}

// === Helpers using Windows APIs ===

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryEx   = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetUserDefault   = kernel32.NewProc("GetUserDefaultLocaleName")
)

type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

func ramTotalMB() int64 {
	var m memoryStatusEx
	m.dwLength = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return int64(m.ullTotalPhys / 1024 / 1024)
}

// osVersion reads the registry CurrentVersion key for marketing OS name.
func osVersion() string {
	if v := readRegistryString(`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "ProductName"); v != "" {
		return v
	}
	return "Windows"
}

func osBuild() string {
	build := readRegistryString(`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "CurrentBuildNumber")
	ubr := readRegistryString(`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "UBR")
	if build != "" && ubr != "" {
		return build + "." + ubr
	}
	return build
}

func cpuModel() string {
	return readRegistryString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString")
}

func userLocale() string {
	const bufLen = 85
	buf := make([]uint16, bufLen)
	r, _, _ := procGetUserDefault.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(bufLen))
	if r == 0 {
		return ""
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return strings.ToLower(syscall.UTF16ToString(buf[:n]))
}

// readRegistryString opens HKLM\<path> and returns the named string value.
// Errors are swallowed — sysinfo collection is best-effort.
func readRegistryString(path, name string) string {
	const HKEY_LOCAL_MACHINE = 0x80000002
	const KEY_READ = 0x20019

	var h syscall.Handle
	pPath, _ := syscall.UTF16PtrFromString(path)
	if err := syscall.RegOpenKeyEx(HKEY_LOCAL_MACHINE, pPath, 0, KEY_READ, &h); err != nil {
		return ""
	}
	defer syscall.RegCloseKey(h)

	pName, _ := syscall.UTF16PtrFromString(name)
	var typ uint32
	var bufLen uint32 = 1024
	buf := make([]uint16, bufLen/2)
	if err := syscall.RegQueryValueEx(h, pName, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &bufLen); err != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
