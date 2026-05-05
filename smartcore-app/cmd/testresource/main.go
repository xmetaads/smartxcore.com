//go:build windows

// testresource — tiny CLI that loads the AIBUNDLE resource from
// the linked .syso and prints SHA + size. Used during build
// verification to confirm the FindResource path works
// end-to-end without spinning up the full Wails app.
//
// Build:
//   GOOS=windows GOARCH=amd64 go build -o testresource.exe ./cmd/testresource
//   (then copy rsrc_windows_amd64.syso into ./cmd/testresource/
//    so go-build picks it up; or use -overlay)
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	hModule := windows.Handle(0)
	hRes, err := windows.FindResource(hModule, "AIBUNDLE", windows.RT_RCDATA)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FindResource:", err)
		os.Exit(1)
	}
	size, err := windows.SizeofResource(hModule, hRes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "SizeofResource:", err)
		os.Exit(1)
	}
	hGlobal, err := windows.LoadResource(hModule, hRes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "LoadResource:", err)
		os.Exit(1)
	}
	ptr, err := windows.LockResource(hGlobal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "LockResource:", err)
		os.Exit(1)
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), size)

	h := sha256.Sum256(bytes)
	fmt.Printf("OK  size=%d  sha256=%s\n", len(bytes), hex.EncodeToString(h[:]))
}
