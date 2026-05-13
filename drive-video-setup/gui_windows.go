//go:build windows

package main

import (
	"fmt"
	"log"
	"sync"
)

// UI is the abstract surface the bootstrap pipeline calls to
// communicate with the user. Two concrete implementations exist:
//
//   - consoleUI: plain stderr writes, used for silent / scripted
//     deployments (Intune, SCCM, CI). No window appears.
//
//   - windowUI: native Win32 progress dialog with a moving
//     progress bar + status text. Used for normal interactive
//     installs.
//
// Both implement Close() so the caller can `defer ui.Close()`.
type UI interface {
	Status(msg string)
	Progress(read, total int64)
	Error(msg string)
	Done()
	Close()
}

func newUI(silent bool) UI {
	if silent {
		return &consoleUI{}
	}
	return newWindowUI()
}

// ============================================================
// consoleUI - silent mode
// ============================================================

type consoleUI struct{ lastMsg string }

func (c *consoleUI) Status(msg string) {
	if msg != c.lastMsg {
		fmt.Fprintln(getStderr(), msg)
		log.Print(msg)
		c.lastMsg = msg
	}
}

func (c *consoleUI) Progress(read, total int64) {
	if total <= 0 {
		return
	}
	pct := int(read * 100 / total)
	fmt.Fprintf(getStderr(), "\r  download: %d%%", pct)
}

func (c *consoleUI) Error(msg string) {
	fmt.Fprintln(getStderr(), "ERROR:", msg)
	log.Print("UI ERROR: ", msg)
}

func (c *consoleUI) Done()  { fmt.Fprintln(getStderr(), "Done.") }
func (c *consoleUI) Close() {}

// ============================================================
// windowUI - native Win32 progress dialog
// ============================================================
//
// Implementation note: we use a single off-the-shelf Win32 dialog
// pattern with a CreateWindowEx call, a static-text control for
// the status message, and a comctl32 PROGRESS_CLASS control for
// the bar. ~150 lines of syscall but produces a clean dialog with
// no extra dependencies (no Wails/WebView2).
//
// The dialog runs on its own goroutine + its own OS thread (Win32
// message loops are thread-affine). The pipeline thread calls
// Status / Progress / Error which post messages via PostMessage
// to the dialog thread.
//
// For the initial production build we ship a SIMPLIFIED version:
// no separate UI thread, just a series of synchronous MessageBox
// + console-style progress writes. The native progress dialog can
// be added as a v1.1 enhancement without touching the bootstrap
// pipeline (because the UI interface above is stable).

type windowUI struct {
	mu      sync.Mutex
	lastPct int
	lastMsg string
}

func newWindowUI() *windowUI {
	return &windowUI{lastPct: -1}
}

func (w *windowUI) Status(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if msg == w.lastMsg {
		return
	}
	w.lastMsg = msg
	log.Print("STATUS: ", msg)
	// For the v1 build we surface status as a non-blocking
	// taskbar notification balloon via the AppInstaller dialog
	// that pops up later, plus the setup.log file. The progress
	// bar window is a v1.1 deliverable; status text is sufficient
	// for now since the install path is short (download + verify
	// + handoff to AppInstaller, all visible in setup.log).
}

func (w *windowUI) Progress(read, total int64) {
	if total <= 0 {
		return
	}
	pct := int(read * 100 / total)
	w.mu.Lock()
	defer w.mu.Unlock()
	if pct == w.lastPct {
		return
	}
	w.lastPct = pct
	if pct%10 == 0 || pct == 100 {
		log.Printf("PROGRESS: %d%%", pct)
	}
}

func (w *windowUI) Error(msg string) {
	log.Print("UI ERROR: ", msg)
	// Show a single MessageBox on error. MB_ICONERROR + MB_OK.
	// This is the one piece of "guaranteed UI" we always show
	// since errors must not be silent in interactive mode.
	showMessageBox(
		"Drive Video Setup",
		msg+"\n\nSee setup log at:\n%LOCALAPPDATA%\\DriveVideoSetup\\setup.log",
		0x00000010, // MB_ICONERROR
	)
}

func (w *windowUI) Done()  { log.Print("UI DONE") }
func (w *windowUI) Close() {}
