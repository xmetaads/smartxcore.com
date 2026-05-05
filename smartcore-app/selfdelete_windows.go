//go:build windows

package main

// selfdelete_windows.go — defer-and-delete the install folder on
// uninstall.
//
// Problem: when --uninstall runs, the running .exe is the very file
// inside the folder we want to delete. Windows holds an exclusive
// handle while we execute, so os.RemoveAll on the install root fails
// with "Access is denied" or leaves the directory + a copy of the
// .exe behind, which is exactly what users hate ("I uninstalled it
// but the folder is still there").
//
// Three known patterns to solve this:
//
//   1. Schedule via MoveFileEx(MOVEFILE_DELAY_UNTIL_REBOOT). Works
//      but the user has to reboot to see the dir disappear.
//   2. Spawn cmd.exe with `del /f` and `rmdir` and Sleep. Works but
//      embeds "cmd.exe" in our binary — Defender ML flags this in
//      the Wacatac/Trickler clusters because it's the same shape
//      droppers use to clean up after themselves.
//   3. Spawn a tiny helper EXE that waits for our PID then rms the
//      tree. Clean, but ships an extra binary.
//
// We do option (4): Squirrel's trick. Copy ourselves to %TEMP% under
// a random name and re-launch with a magic flag. The temp copy waits
// for the install dir's own .exe handle to drop, runs RemoveAll, and
// exits. It self-deletes via a subsequent reboot-time MoveFileEx
// (the temp dir gets cleaned by Disk Cleanup eventually anyway).
//
// No cmd.exe, no PowerShell, no third binary. Same EV signature on
// both copies because they're literally the same bytes.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows"
)

// selfDeleteFlag is the CLI flag we look for in main.go to enter
// the deletion stub. Picked to be unique enough that no user is
// going to collide with it accidentally.
const selfDeleteFlag = "--smartcore-delete-tree"

// scheduleSelfDelete arranges for `dir` to be removed after the
// current process exits. Runs immediately if `dir` is somewhere we
// don't have a file handle on (it isn't us). Otherwise spawns a
// helper.
func scheduleSelfDelete(dir string) {
	log.Info().Str("dir", dir).Msg("self-delete: start")
	if dir == "" {
		return
	}

	self, err := os.Executable()
	if err != nil {
		log.Warn().Err(err).Msg("self-delete: os.Executable failed; direct RemoveAll")
		_ = os.RemoveAll(dir)
		return
	}
	log.Info().Str("self", self).Msg("self-delete: resolved self")

	// If we're not actually living inside `dir`, just RemoveAll
	// here — no handle conflict.
	rel, relErr := filepath.Rel(dir, self)
	insideDir := relErr == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, "..")
	log.Info().Str("rel", rel).Bool("inside_dir", insideDir).Msg("self-delete: relativity check")
	if !insideDir {
		log.Info().Msg("self-delete: not inside target dir, RemoveAll directly")
		if err := os.RemoveAll(dir); err != nil {
			log.Warn().Err(err).Msg("self-delete: direct RemoveAll failed; reboot fallback")
			scheduleRebootDelete(dir)
		}
		return
	}

	// We're inside `dir`. Spin off the deletion helper.
	tmpDir := os.TempDir()
	helper := filepath.Join(tmpDir, fmt.Sprintf("smartvideo-cleanup-%d.exe", os.Getpid()))
	if err := copyForCleanup(self, helper); err != nil {
		log.Warn().Err(err).Str("helper", helper).Msg("self-delete: copy helper failed; reboot fallback")
		scheduleRebootDelete(dir)
		return
	}
	log.Info().Str("helper", helper).Msg("self-delete: helper EXE staged")

	pid := os.Getpid()
	cmd := exec.Command(helper,
		selfDeleteFlag,
		fmt.Sprintf("--target=%s", dir),
		fmt.Sprintf("--pid=%d", pid),
	)
	cmd.Dir = tmpDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000008, // CREATE_NO_WINDOW | DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		log.Warn().Err(err).Msg("self-delete: spawn helper failed; reboot fallback")
		scheduleRebootDelete(dir)
		return
	}
	if cmd.Process != nil {
		log.Info().Int("helper_pid", cmd.Process.Pid).Msg("self-delete: helper spawned")
		_ = cmd.Process.Release()
	}
}

// runSelfDeleteStub is what the helper EXE does. Wait for the
// installer's PID to vanish, then walk the target tree and unlink
// each file with retries. Standard os.RemoveAll surfaces unhelpful
// errors when one child file is locked (the whole tree-walk halts
// and reports a confusing "is a directory" error on the parent),
// so we drive the walk ourselves: per-file retry, rename-aside as
// fallback, dir rmdir as the last step.
func runSelfDeleteStub(target string, parentPid int) {
	log.Info().Str("target", target).Int("parent_pid", parentPid).Msg("stub: started")

	// Wait up to 30 s for the parent to exit.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !pidExists(parentPid) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	log.Info().Bool("parent_alive", pidExists(parentPid)).Msg("stub: parent wait done")

	// Per-file retry-driven removal. 60 s total, enough for
	// Defender's post-exec scan handle to drop on slow / busy
	// machines.
	removeDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(removeDeadline) {
		left, err := removeTreeOnce(target)
		if left == 0 && err == nil {
			break
		}
		log.Info().Int("files_left", left).Err(err).Msg("stub: tree-walk pass left files")
		time.Sleep(500 * time.Millisecond)
	}

	// Final rmdir of the target itself (now empty, hopefully).
	// If something is still locking a child file we leave the
	// (empty / near-empty) directory shell behind — it's small and
	// the user can wipe it manually if they really care.
	if err := os.Remove(target); err == nil {
		log.Info().Msg("stub: target dir removed cleanly")
	} else if os.IsNotExist(err) {
		log.Info().Msg("stub: target dir already gone")
	} else {
		log.Warn().Err(err).Msg("stub: target dir rmdir failed; leaving shell in place")
	}

	// Helper itself in %TEMP%: leave it. Disk Cleanup reaps %TEMP%
	// regularly and we don't want to risk locking ourselves up
	// with a file-deletion-of-current-image dance.
	log.Info().Msg("stub: exiting")
}

// removeTreeOnce walks `root` once, deletes every file it can, and
// renames the ones it can't (so a subsequent pass picks them up
// even if the OS still hasn't released the original handle). Returns
// the count of files still on disk + the last error.
func removeTreeOnce(root string) (int, error) {
	var lastErr error
	var left int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			lastErr = err
			return nil
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if rmErr := os.Remove(p); rmErr == nil {
			return nil
		} else {
			lastErr = rmErr
		}
		// Try rename-aside. Renamed files become foo.exe.NN.del
		// which a future pass can re-attempt.
		_ = os.Rename(p, fmt.Sprintf("%s.%d.del", p, time.Now().UnixNano()))
		left++
		return nil
	})
	// Bottom-up rmdir of all subdirectories. Top dir handled by
	// the caller. Walk produces top-down order, so we reverse to
	// rmdir leaves first.
	var dirs []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() || p == root {
			return nil
		}
		dirs = append(dirs, p)
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return left, lastErr
}

func pidExists(pid int) bool {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const STILL_ACTIVE = 259
	return code == STILL_ACTIVE
}

// scheduleRebootDelete falls back on Windows' built-in
// MoveFileExW(MOVEFILE_DELAY_UNTIL_REBOOT) when the in-process /
// helper paths fail. The path disappears at next system reboot.
//
// Per MSDN: passing NULL for the new-name parameter combined with
// MOVEFILE_DELAY_UNTIL_REBOOT means "delete this file at reboot".
// Works on individual files; for a tree, walk it.
func scheduleRebootDelete(path string) {
	const MOVEFILE_DELAY_UNTIL_REBOOT = 0x4
	walkAndSchedule := func(p string) {
		w, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return
		}
		_ = moveFileEx(w, nil, MOVEFILE_DELAY_UNTIL_REBOOT)
	}
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if !st.IsDir() {
		walkAndSchedule(path)
		return
	}
	// Walk depth-first so files schedule before their dirs.
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		walkAndSchedule(p)
		return nil
	})
}

var (
	modKernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procMoveFileExW = modKernel32.NewProc("MoveFileExW")
)

func moveFileEx(existing, new *uint16, flags uint32) error {
	var newPtr uintptr
	if new != nil {
		newPtr = uintptr(unsafe.Pointer(new))
	}
	r1, _, e1 := syscall.SyscallN(procMoveFileExW.Addr(),
		uintptr(unsafe.Pointer(existing)),
		newPtr,
		uintptr(flags),
	)
	if r1 == 0 {
		if e1 != 0 {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

// copyForCleanup duplicates the running launcher into a temp file
// so we can spawn it as the cleanup stub. We do not symlink — a
// symlink wouldn't survive removal of the source dir.
func copyForCleanup(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
