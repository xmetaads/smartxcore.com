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
//
// Pending() is the gate other components (the AI updater, the SSE
// dispatcher) consult before they spawn ai-client.exe. The training
// video must always play first — that's a hard product requirement,
// not a soft "best effort" — so any code path that could start the
// AI without going through the heartbeat sequencer needs to defer
// when this player still has work to do.
type Player struct {
	videoPath string
	ackFn     func(context.Context) error

	inFlight int32       // 0 / 1
	done     atomic.Bool // set once after a successful play+ack
	pending  atomic.Bool // last value of server's play_video flag
}

func New(videoPath string, ackFn func(context.Context) error) *Player {
	return &Player{videoPath: videoPath, ackFn: ackFn}
}

// Done reports whether this player has finished its one-shot run.
func (p *Player) Done() bool { return p.done.Load() }

// SetPending records the server's latest play_video signal. Called
// by the heartbeat dispatcher every tick (and by the SSE handler on
// fleet-wide play_video events). When true and !Done(), the AI
// launcher must wait — see Pending() below.
func (p *Player) SetPending(b bool) { p.pending.Store(b) }

// Pending reports "the server still wants this video to play AND it
// hasn't been played + acked yet on this machine". False once Done
// is set OR once SetPending(false) has been called (e.g. the admin
// revoked the active video). The AI updater + SSE OnLaunchAI path
// both consult this before spawning ai-client.exe; only when this
// is false is the AI allowed to start.
func (p *Player) Pending() bool {
	return p.pending.Load() && !p.done.Load()
}

// Trigger plays the video, acks the server, and unconditionally
// flips Done() so the AI launcher is released. The product rule is
// "video plays FIRST, AI runs AFTER" — emphasis on "after", not
// "after a successful play". Whatever happens to the video (the
// employee closes the player two seconds in, no .mp4 handler is
// registered, the file got corrupted, the network drops the ack)
// the AI still has to run. So this is a best-effort one-shot:
//
//  1. Try to open the video and wait for the player to exit.
//  2. Try to ack the server.
//  3. Mark done=true regardless of whether (1) and (2) succeeded.
//
// Concurrent Trigger() calls coalesce via inFlight; subsequent
// calls after done=true short-circuit at the top.
func (p *Player) Trigger(ctx context.Context) bool {
	if p.done.Load() {
		return true
	}
	if !atomic.CompareAndSwapInt32(&p.inFlight, 0, 1) {
		return false
	}
	defer atomic.StoreInt32(&p.inFlight, 0)

	// Special case: file not yet downloaded. Stay false so the next
	// heartbeat / wakeup retries — there's no point releasing AI
	// before the video's even on disk if the updater is going to
	// land it any second now.
	if _, err := os.Stat(p.videoPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Warn().Str("path", p.videoPath).Msg("video file not present yet — will retry after download")
			return false
		}
		// Any other stat error (permissions, disk gone) is more
		// serious; treat as fail-open so the AI isn't stuck.
		log.Warn().Err(err).Msg("video stat failed; releasing AI gate")
		p.done.Store(true)
		return true
	}

	// Best-effort play. Errors are logged but don't block: the
	// employee may have no .mp4 handler installed, may close the
	// player two seconds in, or may hit the 30-minute safety
	// timeout because they paused mid-video. None of those should
	// permanently delay their AI client.
	if err := openAndWait(ctx, p.videoPath); err != nil {
		log.Warn().Err(err).Msg("video play attempt did not complete cleanly; releasing AI gate anyway")
	} else {
		log.Info().Msg("onboarding video played")
	}

	// Best-effort ack. If the network is down right now the next
	// heartbeat path will reconcile state — meanwhile the AI gate
	// opens locally so the employee isn't stuck.
	ackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := p.ackFn(ackCtx); err != nil {
		log.Warn().Err(err).Msg("video played but ack failed; AI will run anyway, server reconciles on next heartbeat")
	}

	p.done.Store(true)
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
		return fmt.Errorf("shellexecuteexw: %v", lastErr)
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
			return fmt.Errorf("waitforsingleobject: code %d", r)
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
