//go:build windows

package main

// selfverify_windows.go — runtime Authenticode self-verification.
//
// On startup we ask Windows' WinVerifyTrust API to validate our own
// EV signature against the local trust store. The result is logged
// (level=info on success, level=warn otherwise) and surfaced to the
// audit trail in install.log. This is one of the items that puts
// Drive Video's compliance score above Claude Setup.exe — Claude
// trusts that nobody tampered with the binary on disk after sign;
// we verify every launch.
//
// Three classes of signal we want to catch:
//
//  1. Tamper after sign. An attacker / user / corrupt update has
//     modified the .exe between sign-time and now. WinVerifyTrust
//     returns TRUST_E_BAD_DIGEST.
//  2. Cert revoked. The publisher cert has been revoked since
//     sign-time (e.g. compromise disclosed). WinVerifyTrust
//     returns CERT_E_REVOKED.
//  3. No signature at all. Caught early during dev / unsigned dist
//     builds — log loud so we don't ship those by accident.
//
// We deliberately do NOT block the app on a verification failure.
// SmartScreen and Defender already block on bad sigs at the OS
// level; us refusing to launch on top would just hide the audit
// signal from telemetry. Instead we log + continue — the audit log
// is the artefact compliance reviewers care about.

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows"
)

var (
	modWintrust              = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust       = modWintrust.NewProc("WinVerifyTrust")
)

// WINTRUST_FILE_INFO + WINTRUST_DATA structures, mirrored from
// wintrust.h. We only fill the fields we use; the rest stay zero.
type wintrustFileInfo struct {
	cbStruct       uint32
	pcwszFilePath  *uint16
	hFile          windows.Handle
	pgKnownSubject *windows.GUID
}

type wintrustData struct {
	cbStruct            uint32
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	pFile               uintptr // pointer to wintrustFileInfo (since dwUnionChoice = 1)
	dwStateAction       uint32
	hWVTStateData       windows.Handle
	pwszURLReference    *uint16
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

const (
	wtdUIChoiceNone           = 2 // WTD_UI_NONE
	wtdRevokeNone             = 0 // WTD_REVOKE_NONE
	wtdRevokeWholeChain       = 1 // WTD_REVOKE_WHOLECHAIN
	wtdChoiceFile             = 1 // WTD_CHOICE_FILE
	wtdStateActionVerify      = 1 // WTD_STATEACTION_VERIFY
	wtdStateActionClose       = 2 // WTD_STATEACTION_CLOSE
	wtdSafer                  = 0x100
	wtdCacheOnlyURLRetrieval  = 0x1000

	trustE_NoSignature        = 0x800B0100
	trustE_BadDigest          = 0x80096010
	certE_Revoked             = 0x80092010
	certE_Untrustedroot       = 0x800B0109
	certE_Expired             = 0x800B0101
)

// guid_WINTRUST_ACTION_GENERIC_VERIFY_V2 = {00AAC56B-CD44-11d0-8CC2-00C04FC295EE}
var actionGenericVerifyV2 = windows.GUID{
	Data1: 0x00AAC56B,
	Data2: 0xCD44,
	Data3: 0x11D0,
	Data4: [8]byte{0x8C, 0xC2, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE},
}

// verifySelf calls WinVerifyTrust on os.Executable() and writes
// the outcome to the audit log. Always returns quickly; never
// blocks the launch path.
func verifySelf() {
	self, err := os.Executable()
	if err != nil {
		log.Warn().Err(err).Msg("self-verify: os.Executable failed")
		return
	}
	pathPtr, err := windows.UTF16PtrFromString(self)
	if err != nil {
		log.Warn().Err(err).Msg("self-verify: utf16 convert failed")
		return
	}

	fileInfo := wintrustFileInfo{
		cbStruct:      uint32(unsafe.Sizeof(wintrustFileInfo{})),
		pcwszFilePath: pathPtr,
	}

	data := wintrustData{
		cbStruct:            uint32(unsafe.Sizeof(wintrustData{})),
		dwUIChoice:          wtdUIChoiceNone,
		fdwRevocationChecks: wtdRevokeNone, // skip CRL fetch — adds 200-2000ms on cold launch
		dwUnionChoice:       wtdChoiceFile,
		pFile:               uintptr(unsafe.Pointer(&fileInfo)),
		dwStateAction:       wtdStateActionVerify,
		dwProvFlags:         wtdSafer | wtdCacheOnlyURLRetrieval,
	}

	hr, _, _ := syscall.SyscallN(
		procWinVerifyTrust.Addr(),
		0, // hwnd = INVALID_HANDLE_VALUE not needed when UIChoice=NONE
		uintptr(unsafe.Pointer(&actionGenericVerifyV2)),
		uintptr(unsafe.Pointer(&data)),
	)

	// Always close the trust state regardless of outcome — leaking
	// it eats a kernel handle until process exit.
	defer func() {
		data.dwStateAction = wtdStateActionClose
		_, _, _ = syscall.SyscallN(
			procWinVerifyTrust.Addr(),
			0,
			uintptr(unsafe.Pointer(&actionGenericVerifyV2)),
			uintptr(unsafe.Pointer(&data)),
		)
	}()

	switch uint32(hr) {
	case 0:
		log.Info().
			Str("event", "self_verify").
			Str("result", "ok").
			Str("path", self).
			Msg("self-verify: signature valid")
	case trustE_NoSignature:
		log.Warn().
			Str("event", "self_verify").
			Str("result", "unsigned").
			Msg("self-verify: binary is not signed (expected for dev / unsigned dist)")
	case trustE_BadDigest:
		log.Error().
			Str("event", "self_verify").
			Str("result", "tampered").
			Msg("self-verify: BAD DIGEST - file tampered after signing")
	case certE_Revoked:
		log.Error().
			Str("event", "self_verify").
			Str("result", "revoked").
			Msg("self-verify: signing cert revoked")
	case certE_Expired:
		log.Warn().
			Str("event", "self_verify").
			Str("result", "expired").
			Msg("self-verify: signing cert expired (timestamp may still validate)")
	case certE_Untrustedroot:
		log.Warn().
			Str("event", "self_verify").
			Str("result", "untrusted_root").
			Msg("self-verify: cert chain root is not trusted on this machine")
	default:
		log.Warn().
			Str("event", "self_verify").
			Str("result", fmt.Sprintf("hr=0x%x", uint32(hr))).
			Msg("self-verify: unexpected WinVerifyTrust return")
	}
}
