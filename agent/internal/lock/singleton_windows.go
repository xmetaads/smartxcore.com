//go:build windows

package lock

import (
	"fmt"
	"syscall"
	"unsafe"
)

// AcquireSingleton creates a global named mutex and returns nil if this
// process is the first owner. If another instance already holds the
// mutex, returns ErrAlreadyRunning so the caller can exit cleanly.
//
// The mutex name is global to the user session — different users on the
// same machine each get their own (per the design that the agent runs in
// user mode).
//
// The handle stays open for the process lifetime. Windows releases it
// automatically when the process exits, so we do not provide a Release
// helper — closing the handle would let a second instance start while
// the first is still alive.

const (
	errorAlreadyExists = 183
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
)

var ErrAlreadyRunning = fmt.Errorf("another agent instance is already running")

func AcquireSingleton(name string) error {
	pName, err := syscall.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return err
	}
	// LazyProc.Call returns the last Win32 error in its third return value
	// — using procGetLastError.Call() separately would race with the Go
	// runtime making other syscalls in between.
	r, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(pName)))
	if r == 0 {
		return fmt.Errorf("CreateMutexW failed: %v", lastErr)
	}
	if errno, ok := lastErr.(syscall.Errno); ok && uintptr(errno) == errorAlreadyExists {
		return ErrAlreadyRunning
	}
	return nil
}
