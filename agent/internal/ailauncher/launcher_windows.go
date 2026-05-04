//go:build windows

// Package ailauncher spawns the AI agent's entrypoint as a DETACHED
// child process running with the current user's privileges.
//
// Smartcore is a one-shot installer: it fetches the install config,
// extracts the AI bundle, and invokes SpawnDetached once. There is
// no service, no LocalSystem token, and no parent-child supervision.
// The AI agent runs as the user who ran Smartcore.exe — that is the
// whole point of this redesign. Earlier revisions invoked the AI
// from a Windows service, which gave it a SYSTEM token and broke
// every AI feature that needed access to the user's desktop, browser,
// files, etc. Spawning from this user-mode process lets the AI
// behave like every other user app on the machine.
//
// Detachment flags (DETACHED_PROCESS | CREATE_NO_WINDOW) ensure that
// the AI process keeps running after Smartcore exits — the file
// handle Windows holds on Smartcore.exe is released the moment we
// return from main.
package ailauncher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/rs/zerolog/log"
)

// SpawnTarget is what installer's marker resolves to: where the AI
// entrypoint lives on disk, and what working directory it expects.
type SpawnTarget struct {
	Path string // absolute path to the entrypoint EXE
	CWD  string // working directory; usually filepath.Dir(Path)
}

// SpawnDetached launches target.Path as a detached child process.
// The child inherits the current user's token (no UAC, no SYSTEM).
// Returns once the child has started — does NOT Wait() on it.
//
// CreationFlags = DETACHED_PROCESS (0x08) | CREATE_NO_WINDOW (0x08000000):
//
//   - DETACHED_PROCESS: child has no console of its own and is not
//     attached to Smartcore's console. When Smartcore exits seconds
//     later, the child is unaffected.
//   - CREATE_NO_WINDOW: no console window flashes on screen during
//     the brief moment Smartcore's main goroutine is still alive.
func SpawnDetached(target SpawnTarget) error {
	if target.Path == "" {
		return errors.New("empty spawn target")
	}
	if _, err := os.Stat(target.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("entrypoint not present at %s", target.Path)
		}
		return fmt.Errorf("stat entrypoint: %w", err)
	}

	cwd := target.CWD
	if cwd == "" {
		cwd = filepath.Dir(target.Path)
	}

	cmd := exec.Command(target.Path)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000008,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	pid := -1
	if cmd.Process != nil {
		pid = cmd.Process.Pid
		// Release the handle so the OS can reap the process when it
		// exits. Smartcore is not tracking lifecycle.
		_ = cmd.Process.Release()
	}
	log.Info().Int("pid", pid).Str("bin", target.Path).Msg("AI agent spawned (detached, user privileges)")
	return nil
}
