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
// Implementation walks the standard FindResource / LoadResource
// / LockResource chain via golang.org/x/sys/windows (which
// already has the right Go-friendly wrappers). The returned
// []byte aliases directly into the loaded image's .rsrc
// section - no copy, no allocation, just a slice header
// pointing at the bytes the loader has already mapped into
// memory. The slice is valid for the lifetime of the process;
// we never write through it.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// resourceNameAIBundle is the resource name we wrote into
// winres.json. Looked up by name (UTF-16) rather than ID so
// the declaration reads literally and adding more resources
// later doesn't risk colliding numeric IDs.
const resourceNameAIBundle = "AIBUNDLE"

// loadEmbeddedAIBundle returns a slice aliasing the AI bundle
// bytes inside the running module's .rsrc section. Returns
// (nil, error) if the resource is missing - which can only
// happen if the build was produced without the go-winres
// pipeline (e.g. someone called `go build` directly instead of
// build-clean.ps1).
func loadEmbeddedAIBundle() ([]byte, error) {
	hModule := windows.Handle(0) // NULL = current module

	// FindResource accepts ResourceIDOrString for both name and
	// type. We pass:
	//   - resourceNameAIBundle as a Go string (matches the
	//     Unicode-keyed name in winres.json)
	//   - windows.RT_RCDATA which is the predefined ResourceID(10)
	hRes, err := windows.FindResource(hModule, resourceNameAIBundle, windows.RT_RCDATA)
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
