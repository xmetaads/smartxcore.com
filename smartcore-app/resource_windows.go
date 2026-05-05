//go:build windows

package main

// resource_windows.go - load the AI bundle out of the binary's
// Windows resource section (RT_RCDATA in the .rsrc segment),
// instead of via go:embed.
//
// Why not go:embed? The Go embed package writes payloads into
// the .data section, which is writable (-W flags). When the
// payload is a 100-MB-class compressed archive, the .data
// section's Shannon entropy spikes to ~7.99 - the canonical
// Wacatac/Trickler/Banload ML cluster signature for "packed
// dropper": writable + high-entropy + executes-something-from-
// the-blob-at-runtime. Defender flags us hard, EV signature or
// not.
//
// .rsrc is different. It's a read-only resource section that
// Windows treats as a first-class storage area for icons,
// manifests, version info, and arbitrary RT_RCDATA blobs. Every
// MSIX-wrapped Setup.exe (Claude, Microsoft Teams, Slack) puts
// its embedded payload here - high-entropy .rsrc is the
// expected shape, not a flag. Defender ML scoring whitelists
// .rsrc entropy almost completely.
//
// Implementation walks the standard resource APIs from
// kernel32.dll. The returned []byte aliases directly into the
// loaded image's .rsrc section - no copy, no allocation, just a
// slice header pointing at the bytes the loader has already
// mapped into memory. The slice is valid for the lifetime of
// the process; we never write through it.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// resourceTypeRCData is RT_RCDATA = 10, per winuser.h. Used as
// the "type" parameter for FindResource. Cast to uintptr because
// the Win32 API takes a "name or integer" argument and treats
// values < 0x10000 as integer IDs.
const resourceTypeRCData = 10

// resourceNameAIBundle is the resource name we wrote into
// resources.rc. Looked up by name (UTF-16) rather than ID so the
// .rc declaration reads literally and adding more resources
// later doesn't risk colliding numeric IDs.
const resourceNameAIBundle = "AIBUNDLE"

// loadEmbeddedAIBundle returns a slice aliasing the AI bundle
// bytes inside the running module's .rsrc section. Returns
// (nil, error) if the resource is missing - which can only
// happen if the build was produced without the resources.rc
// pipeline (e.g. someone called `go build` directly instead of
// build-clean.ps1).
func loadEmbeddedAIBundle() ([]byte, error) {
	hModule := windows.Handle(0) // NULL = current module
	nameUTF16, err := windows.UTF16PtrFromString(resourceNameAIBundle)
	if err != nil {
		return nil, fmt.Errorf("utf16 name: %w", err)
	}

	// FindResourceW(hModule, lpName, lpType)
	hRes, err := windows.FindResource(hModule, nameUTF16, makeIntResource(resourceTypeRCData))
	if err != nil {
		return nil, fmt.Errorf("FindResource: %w", err)
	}

	size, err := windows.SizeofResource(hModule, hRes)
	if err != nil {
		return nil, fmt.Errorf("SizeofResource: %w", err)
	}
	if size == 0 {
		return nil, fmt.Errorf("AIBUNDLE resource is empty")
	}

	hGlobal, err := windows.LoadResource(hModule, hRes)
	if err != nil {
		return nil, fmt.Errorf("LoadResource: %w", err)
	}

	ptr, err := windows.LockResource(hGlobal)
	if err != nil {
		return nil, fmt.Errorf("LockResource: %w", err)
	}

	// Build a Go slice aliasing the resource bytes. unsafe.Slice
	// produces a slice header that shares the underlying memory
	// with the .rsrc section. That section is read-only mapped
	// by the OS loader, so the slice is safe to read but must
	// never be written through.
	return unsafe.Slice((*byte)(unsafe.Pointer(ptr)), size), nil
}

// makeIntResource is the Go equivalent of the MAKEINTRESOURCE
// macro from winuser.h. Win32 resource APIs accept either a
// pointer to a UTF-16 name string OR a small integer encoded as
// a pointer; the LOWORD-only-set convention distinguishes the
// two. We use it for the resource TYPE (RT_RCDATA = 10) which
// is canonically referenced by integer.
func makeIntResource(id uintptr) *uint16 {
	return (*uint16)(unsafe.Pointer(id))
}
