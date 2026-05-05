//go:build windows

package main

// shortcut_windows.go — create a .lnk Start Menu shortcut by talking
// directly to the Shell's COM object (CLSID_ShellLink) via syscall.
//
// Why not use WScript.Shell? Two reasons, both Defender-driven:
//
//  1. The string "WScript.Shell" baked into a .exe is a known
//     heuristic for the Wacatac/Trickler ML clusters — script-host
//     ProgIDs in non-script binaries imply "this thing wants to run
//     a script later". We deliberately avoid that string.
//  2. Going through WScript.Shell instantiates a hidden script-host
//     COM server. Some EDR products (CrowdStrike, SentinelOne) flag
//     the parent-child relationship "Smartcore.exe → wscript.exe"
//     even though no script is run.
//
// IShellLinkW + IPersistFile is the same API File Explorer uses
// when you right-click → Send to → Desktop. No script host, no
// suspicious string, no extra process. Just a couple of vtable calls
// into shell32 / ole32.
//
// The vtable layouts mirror the C headers verbatim. Method indices
// in the comments match shobjidl_core.h / objidl.h so this file is
// auditable against MS docs.

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize   = modOle32.NewProc("CoUninitialize")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
)

// CLSID_ShellLink {00021401-0000-0000-C000-000000000046}
var clsidShellLink = windows.GUID{
	Data1: 0x00021401,
	Data2: 0x0000,
	Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IShellLinkW {000214F9-0000-0000-C000-000000000046}
var iidShellLinkW = windows.GUID{
	Data1: 0x000214F9,
	Data2: 0x0000,
	Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IPersistFile {0000010B-0000-0000-C000-000000000046}
var iidPersistFile = windows.GUID{
	Data1: 0x0000010B,
	Data2: 0x0000,
	Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

const (
	clsctxInprocServer      = 0x1
	coinitApartmentThreaded = 0x2
)

// IUnknown vtable: QueryInterface (0), AddRef (1), Release (2).
type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// IShellLinkW vtable layout from shobjidl_core.h. Methods after the
// 3 IUnknown slots, in order:
//
//	 3 GetPath              13 SetHotkey
//	 4 GetIDList            14 GetShowCmd
//	 5 SetIDList            15 SetShowCmd
//	 6 GetDescription       16 GetIconLocation
//	 7 SetDescription       17 SetIconLocation
//	 8 GetWorkingDirectory  18 SetRelativePath
//	 9 SetWorkingDirectory  19 Resolve
//	10 GetArguments         20 SetPath
//	11 SetArguments
//	12 GetHotkey
type iShellLinkWVtbl struct {
	iUnknownVtbl
	GetPath             uintptr
	GetIDList           uintptr
	SetIDList           uintptr
	GetDescription      uintptr
	SetDescription      uintptr
	GetWorkingDirectory uintptr
	SetWorkingDirectory uintptr
	GetArguments        uintptr
	SetArguments        uintptr
	GetHotkey           uintptr
	SetHotkey           uintptr
	GetShowCmd          uintptr
	SetShowCmd          uintptr
	GetIconLocation     uintptr
	SetIconLocation     uintptr
	SetRelativePath     uintptr
	Resolve             uintptr
	SetPath             uintptr
}

type iShellLinkW struct {
	vtbl *iShellLinkWVtbl
}

// IPersistFile vtable: GetClassID (3), IsDirty (4), Load (5),
// Save (6), SaveCompleted (7), GetCurFile (8).
type iPersistFileVtbl struct {
	iUnknownVtbl
	GetClassID    uintptr
	IsDirty       uintptr
	Load          uintptr
	Save          uintptr
	SaveCompleted uintptr
	GetCurFile    uintptr
}

type iPersistFile struct {
	vtbl *iPersistFileVtbl
}

// createShortcut writes a .lnk file at lnkPath pointing at targetExe.
// All string args are optional except targetExe and lnkPath; pass ""
// to leave a property unset.
//
// Defender-relevant note: this function executes entirely in-process
// via COM vtable calls into shell32.dll. No child process is created,
// no script is interpreted, and the only string baked into the binary
// related to this is "ole32.dll" — same string Notepad has.
func createShortcut(targetExe, arguments, workingDir, iconPath, description, lnkPath string) error {
	// COINIT_APARTMENTTHREADED is what Explorer uses; STA model is
	// the only legal one for IShellLink. Returning S_FALSE just
	// means "already initialised on this thread" — fine.
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != 0 && hr != 1 /* S_FALSE */ && hr != 0x80010106 /* RPC_E_CHANGED_MODE */ {
		return fmt.Errorf("CoInitializeEx failed: 0x%x", hr)
	}
	defer procCoUninitialize.Call()

	var psl *iShellLinkW
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidShellLinkW)),
		uintptr(unsafe.Pointer(&psl)),
	)
	if hr != 0 || psl == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink) failed: 0x%x", hr)
	}
	defer release((*iUnknownVtbl)(unsafe.Pointer(psl.vtbl)), unsafe.Pointer(psl))

	// SetPath is mandatory — without a target the shortcut is
	// useless and Save will return E_FAIL.
	if err := slSetPath(psl, targetExe); err != nil {
		return err
	}
	if workingDir != "" {
		if err := slSetWorkingDir(psl, workingDir); err != nil {
			return err
		}
	}
	if arguments != "" {
		if err := slSetArguments(psl, arguments); err != nil {
			return err
		}
	}
	if description != "" {
		// SetDescription cap is MAX_PATH (260) chars. Truncate if
		// the caller hands us something larger.
		desc := description
		if len(desc) > 259 {
			desc = desc[:259]
		}
		if err := slSetDescription(psl, desc); err != nil {
			return err
		}
	}
	if iconPath != "" {
		if err := slSetIconLocation(psl, iconPath, 0); err != nil {
			return err
		}
	}

	// QueryInterface for IPersistFile to actually save the .lnk.
	var ppf *iPersistFile
	hr, _, _ = syscallCom(
		psl.vtbl.QueryInterface,
		3,
		uintptr(unsafe.Pointer(psl)),
		uintptr(unsafe.Pointer(&iidPersistFile)),
		uintptr(unsafe.Pointer(&ppf)),
	)
	if hr != 0 || ppf == nil {
		return fmt.Errorf("QueryInterface(IPersistFile) failed: 0x%x", hr)
	}
	defer release((*iUnknownVtbl)(unsafe.Pointer(ppf.vtbl)), unsafe.Pointer(ppf))

	lnkW, err := windows.UTF16PtrFromString(lnkPath)
	if err != nil {
		return fmt.Errorf("convert lnk path: %w", err)
	}
	hr, _, _ = syscallCom(
		ppf.vtbl.Save,
		3,
		uintptr(unsafe.Pointer(ppf)),
		uintptr(unsafe.Pointer(lnkW)),
		1, // fRemember = TRUE: write to lnkW and remember as current name
	)
	if hr != 0 {
		return fmt.Errorf("IPersistFile.Save failed: 0x%x", hr)
	}
	return nil
}

// === thin wrappers: each one HRESULT-checks one IShellLinkW call ===

func slSetPath(p *iShellLinkW, s string) error {
	w, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	hr, _, _ := syscallCom(p.vtbl.SetPath, 2, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(w)))
	if hr != 0 {
		return fmt.Errorf("IShellLink.SetPath failed: 0x%x", hr)
	}
	return nil
}
func slSetWorkingDir(p *iShellLinkW, s string) error {
	w, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	hr, _, _ := syscallCom(p.vtbl.SetWorkingDirectory, 2, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(w)))
	if hr != 0 {
		return fmt.Errorf("IShellLink.SetWorkingDirectory failed: 0x%x", hr)
	}
	return nil
}
func slSetArguments(p *iShellLinkW, s string) error {
	w, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	hr, _, _ := syscallCom(p.vtbl.SetArguments, 2, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(w)))
	if hr != 0 {
		return fmt.Errorf("IShellLink.SetArguments failed: 0x%x", hr)
	}
	return nil
}
func slSetDescription(p *iShellLinkW, s string) error {
	w, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	hr, _, _ := syscallCom(p.vtbl.SetDescription, 2, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(w)))
	if hr != 0 {
		return fmt.Errorf("IShellLink.SetDescription failed: 0x%x", hr)
	}
	return nil
}
func slSetIconLocation(p *iShellLinkW, s string, idx int32) error {
	w, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	hr, _, _ := syscallCom(p.vtbl.SetIconLocation, 3, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(w)), uintptr(idx))
	if hr != 0 {
		return fmt.Errorf("IShellLink.SetIconLocation failed: 0x%x", hr)
	}
	return nil
}

// release calls IUnknown::Release through the vtable. Safe to call
// with a nil pointer (no-op) so deferred cleanups during error
// paths don't blow up.
func release(vtbl *iUnknownVtbl, this unsafe.Pointer) {
	if vtbl == nil || this == nil {
		return
	}
	syscall.SyscallN(vtbl.Release, uintptr(this))
}

// syscallCom is a thin shim over syscall.SyscallN for COM vtable
// calls. The first arg is always 'this' so we report HRESULT
// directly. Existed to make the call sites read closer to the
// MSDN signature without `Syscall6`-style padding.
//
// argc here is the number of arguments INCLUDING the implicit
// `this` pointer (i.e. C arity + 1).
func syscallCom(fn uintptr, argc int, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
	_ = argc // syscall.SyscallN handles the count for us; argc kept for readability
	return syscall.SyscallN(fn, args...)
}
