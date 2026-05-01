//go:build windows

// Package videoplay opens the onboarding video file in whatever
// program is registered as the default handler for .mp4 on the
// employee's machine (Movies & TV, VLC, MPC-HC, …) and waits for
// that program to exit before reporting completion.
//
// We deliberately use ShellExecuteExW — the same Win32 API every
// double-click in Explorer goes through. No shell command, no
// LOLBAS string, no third-party media library bundled. The agent
// binary stays at 0/12 forbidden-strings and the OS treats
// "open .mp4" as ordinary user behaviour.
package videoplay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/rs/zerolog/log"
)

// Player runs the one-shot "play video then ack" sequence. Like the
// AI launcher it's single-fire: once a video has played and the
// server-side ack has succeeded, Done() returns true and Trigger()
// no-ops forever after.
type Player struct {
	videoPath string
	ackFn     func(context.Context) error

	inFlight int32 // 0 / 1
	done     atomic.Bool
}

func New(videoPath string, ackFn func(context.Context) error) *Player {
	return &Player{videoPath: videoPath, ackFn: ackFn}
}

// Done reports whether this player has finished its one-shot run.
func (p *Player) Done() bool { return p.done.Load() }

// Trigger plays the video and acks the server. Returns true on full
// success. Concurrent Trigger() calls coalesce — only one play
// attempt is in flight at a time.
//
// Caller is expected to call this in a goroutine; the function
// blocks until the video player process exits (or a 30-minute
// safety timeout fires, whichever is first).
func (p *Player) Trigger(ctx context.Context) bool {
	if p.done.Load() {
		return true
	}
	if !atomic.CompareAndSwapInt32(&p.inFlight, 0, 1) {
		return false
	}
	defer atomic.StoreInt32(&p.inFlight, 0)

	if _, err := os.Stat(p.videoPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Warn().Str("path", p.videoPath).Msg("video file not present yet — will retry after download")
			return false
		}
		log.Warn().Err(err).Msg("video stat failed")
		return false
	}

	if err := openAndWait(ctx, p.videoPath); err != nil {
		log.Warn().Err(err).Msg("video play attempt failed; will retry on next heartbeat")
		return false
	}

	ackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := p.ackFn(ackCtx); err != nil {
		log.Warn().Err(err).Msg("video played but ack failed; will re-ack on next trigger")
		return false
	}
	p.done.Store(true)
	log.Info().Msg("onboarding video played and ack'd (one-shot complete)")
	return true
}

// openAndWait opens videoPath in the system's default .mp4 handler
// via ShellExecuteExW with SEE_MASK_NOCLOSEPROCESS so we receive a
// process handle to wait on. Bounded by a 30-minute safety timeout
// so a hung player never traps the agent.
//
// SHELLEXECUTEINFOW layout (W = wide-char) is documented at
// https://learn.microsoft.com/windows/win32/api/shellapi/ns-shellapi-shellexecuteinfow
func openAndWait(ctx context.Context, path string) error {
	verb, _ := syscall.UTF16PtrFromString("open")
	target, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode path: %w", err)
	}

	info := shellExecuteInfoW{
		Verb:    verb,
		File:    target,
		ShowCmd: 1, // SW_SHOWNORMAL
		// SEE_MASK_NOCLOSEPROCESS (0x40): keep hProcess so we can
		// wait on it.
		// SEE_MASK_FLAG_NO_UI    (0x400): suppress error dialog
		// when no .mp4 handler is registered; we get the failure
		// in the return value instead.
		Mask: 0x40 | 0x400,
	}
	info.Size = uint32(unsafe.Sizeof(info))

	shell32 := syscall.NewLazyDLL("shell32.dll")
	procShellExec := shell32.NewProc("ShellExecuteExW")

	r1, _, lastErr := procShellExec.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return fmt.Errorf("ShellExecuteExW: %v", lastErr)
	}

	// Some apps (UWP wrappers like Movies & TV) launch a host process
	// that hands off to a different handler and returns
	// HInstApp == 33 with no hProcess. Treat that as "we tried,
	// can't follow lifecycle, mark done immediately".
	if info.HProcess == 0 {
		log.Info().Msg("video player launched without trackable process handle; treating as done")
		return nil
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procWait := kernel32.NewProc("WaitForSingleObject")
	procClose := kernel32.NewProc("CloseHandle")
	defer procClose.Call(info.HProcess)

	// 30-minute safety cap so a forgotten paused player can't
	// permanently block AI launch.
	const maxWait = 30 * time.Minute
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || time.Until(deadline) > maxWait {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, maxWait)
		defer cancel()
	}

	// Poll WaitForSingleObject in 1-second slices so context
	// cancellation propagates without stranding the goroutine.
	for {
		// Check ctx first so cancellation is observed even when
		// the video player exited at the same instant.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		r, _, _ := procWait.Call(info.HProcess, 1000) // 1s timeout
		switch r {
		case 0: // WAIT_OBJECT_0 — process exited
			return nil
		case 0x102: // WAIT_TIMEOUT — keep polling
			continue
		default:
			return fmt.Errorf("WaitForSingleObject: code %d", r)
		}
	}
}

// shellExecuteInfoW is the wide-char SHELLEXECUTEINFO struct. Layout
// matters — Windows reads it by offset.
type shellExecuteInfoW struct {
	Size         uint32
	Mask         uint32
	Hwnd         uintptr
	Verb         *uint16
	File         *uint16
	Parameters   *uint16
	Directory    *uint16
	ShowCmd      int32
	HInstApp     uintptr
	IDList       uintptr
	Class        *uint16
	HKeyClass    uintptr
	HotKey       uint32
	HIconOrMonitor uintptr
	HProcess     uintptr
}
