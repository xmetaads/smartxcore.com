//go:build windows

package main

import "os/exec"

// newCommand wraps exec.Command so future tweaks (e.g. SysProcAttr to hide
// child windows) live in one spot.
func newCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
