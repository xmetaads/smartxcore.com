//go:build windows

package main

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Native Win32 Media Player-styled installer GUI. No third-party deps.
// The window has a header, a "video preview" area, a status line, and
// two buttons (Play Video / Close). Clicking Play runs the agent
// install on a background goroutine and updates the status field as
// it progresses.
//
// Why not InputBox/wscript any more: employees can mistype a code, hit
// the wrong key, or paste leading whitespace. Eliminating the text
// path removes the most common support category ("I typed PLAY but it
// didn't work"). The deployment code is baked into the installer at
// build time via -ldflags "-X main.deploymentCode=PLAY".

const (
	winClassName = "SmartcoreInstallerWindow"
	winTitle     = "Media Player"

	idPlayBtn   = 100
	idCloseBtn  = 101
	idStatusLbl = 102

	// Window styles
	wsOverlapped       = 0x00000000
	wsCaption          = 0x00C00000
	wsSysMenu          = 0x00080000
	wsMinimizeBox      = 0x00020000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsTabStop          = 0x00010000
	wsClipChildren     = 0x02000000
	bsDefPushButton    = 0x00000001
	bsPushButton       = 0x00000000
	ssLeft             = 0x00000000
	ssCenter           = 0x00000001
	ssLeftNoWordWrap   = 0x0000000C
	cwUseDefault       = 0x80000000
	swShow             = 5
	swShowNormal       = 1
	wmCommand          = 0x0111
	wmClose            = 0x0010
	wmDestroy          = 0x0002
	wmPaint            = 0x000F
	wmCtlColorStatic   = 0x0138
	wmCtlColorBtn      = 0x0135
	wmSetFont          = 0x0030
	wmGetMinMaxInfo    = 0x0024
	wmEraseBkgnd       = 0x0014
	wmAppInstallDone   = 0x8001 // custom: posted from worker goroutine
	wmAppStatusUpdate  = 0x8002
	cosmeticsHeaderH   = 90
	cosmeticsFooterH   = 110
	colorBackground    = 0x00FFFFFF // white
	colorVideoArea     = 0x002D2D2D // dark gray
	colorVideoText     = 0x00FFFFFF // white
	idiApplication     = 32512
	idcArrow           = 32512
	whiteBrush         = 0
)

// nullTerm is what we pass for `cchText` to DrawTextW when we want it
// to compute the length itself (treat the buffer as null-terminated).
// Win32 documents this as `-1` but Go disallows narrowing a negative
// constant into uintptr, so use the two's-complement equivalent: all
// ones, which the API decodes as -1 on both 32- and 64-bit builds.
var nullTerm = ^uintptr(0)

// user32 is already declared in gui_windows.go. Declare gdi32 and
// kernel32 fresh; reuse user32 for the procs we need here.
var (
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procSetWindowTextW   = user32.NewProc("SetWindowTextW")
	procEnableWindow     = user32.NewProc("EnableWindow")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procFillRect         = user32.NewProc("FillRect")
	procDrawTextW        = user32.NewProc("DrawTextW")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procSetWindowPos     = user32.NewProc("SetWindowPos")

	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSelectObject     = gdi32.NewProc("SelectObject")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

// installState lets the worker goroutine and the UI thread coordinate.
// installing transitions 0→1 on Play click; result holds the final
// outcome ("" = success, otherwise an error message).
type installState struct {
	installing atomic.Int32
	hwnd       uintptr
	statusHwnd uintptr
	playHwnd   uintptr
	closeHwnd  uintptr
	hFont      uintptr
	hFontBig   uintptr
	hFontMid   uintptr
	hBrushVid  uintptr
	resultMsg  atomic.Pointer[string]

	// install closure: invoked when Play is clicked. Returns nil on
	// success, error otherwise. Lives here so the window proc can
	// reach it from the worker goroutine.
	doInstall func() error
}

var state installState

// showMediaPlayerInstaller is the entry point. It spins up the Media
// Player window and blocks until the user closes it. doInstall is
// invoked on a worker goroutine when the Play button is clicked.
func showMediaPlayerInstaller(doInstall func() error) error {
	state.doInstall = doInstall

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	if hInstance == 0 {
		return fmt.Errorf("get module handle failed")
	}

	classNamePtr, _ := syscall.UTF16PtrFromString(winClassName)
	titlePtr, _ := syscall.UTF16PtrFromString(winTitle)

	cursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	icon, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))

	bgBrush, _, _ := procCreateSolidBrush.Call(uintptr(colorBackground))
	state.hBrushVid, _, _ = procCreateSolidBrush.Call(uintptr(colorVideoArea))

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         0x0003, // CS_HREDRAW | CS_VREDRAW
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInstance,
		HIcon:         icon,
		HCursor:       cursor,
		HbrBackground: bgBrush,
		LpszClassName: classNamePtr,
	}
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register class failed")
	}

	// Center the window on the primary monitor.
	const winW, winH = 820, 540
	screenW, _, _ := procGetSystemMetrics.Call(0)
	screenH, _, _ := procGetSystemMetrics.Call(1)
	x := (int32(screenW) - winW) / 2
	y := (int32(screenH) - winH) / 2

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(wsCaption|wsSysMenu|wsMinimizeBox|wsClipChildren),
		uintptr(x), uintptr(y), uintptr(winW), uintptr(winH),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("create window failed")
	}
	state.hwnd = hwnd

	procShowWindow.Call(hwnd, uintptr(swShow))
	procUpdateWindow.Call(hwnd)

	// Standard Win32 message pump. Runs on the goroutine that called
	// us — must stay locked to the OS thread that created the window.
	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	// Cleanup brushes — small but tidy.
	procDeleteObject.Call(state.hBrushVid)
	procDeleteObject.Call(bgBrush)
	if state.hFont != 0 {
		procDeleteObject.Call(state.hFont)
	}
	if state.hFontBig != 0 {
		procDeleteObject.Call(state.hFontBig)
	}
	if state.hFontMid != 0 {
		procDeleteObject.Call(state.hFontMid)
	}

	if p := state.resultMsg.Load(); p != nil && *p != "" {
		return fmt.Errorf("%s", *p)
	}
	return nil
}

// wndProc is the window's message handler. Routes button clicks,
// install-done custom messages, paint requests, and close events.
func wndProc(hwnd, uMsg, wParam, lParam uintptr) uintptr {
	switch uMsg {
	case 0x0001: // WM_CREATE — create child controls
		createControls(hwnd)
		return 0

	case wmPaint:
		paintWindow(hwnd)
		return 0

	case wmEraseBkgnd:
		// Default eraser uses the white background brush we set on the
		// class — fine for header/footer. Video area is repainted in
		// WM_PAINT so it's not flickery.
		return 1

	case wmCtlColorStatic:
		// Status label: transparent text on white background.
		hdc := wParam
		procSetBkMode.Call(hdc, 1) // TRANSPARENT
		procSetTextColor.Call(hdc, 0x00606060)
		// Returning a stock white brush keeps the bg painted.
		hb, _, _ := procCreateSolidBrush.Call(uintptr(colorBackground))
		return hb

	case wmCommand:
		switch loword(uint32(wParam)) {
		case idPlayBtn:
			handlePlayClick()
		case idCloseBtn:
			procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
		}
		return 0

	case wmAppStatusUpdate:
		// lParam is a pointer to a Go string we kept alive via state.
		setStatus(state.statusHwnd, *state.resultMsg.Load())
		return 0

	case wmAppInstallDone:
		// wParam: 0 = success, 1 = failure
		if wParam == 0 {
			// Success — close the window; main() shows the success box.
			procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
		} else {
			// Failure — re-enable Play so the user can retry.
			procEnableWindow.Call(state.playHwnd, 1)
		}
		return 0

	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, uMsg, wParam, lParam)
	return r
}

func createControls(hwnd uintptr) {
	hInstance, _, _ := procGetModuleHandleW.Call(0)

	// Fonts. CreateFontW: -height in logical units, 0 width, 0 escape,
	// 0 orient, weight, italic=0, underline=0, strikeout=0, charset=1
	// (DEFAULT_CHARSET), out=0, clip=0, qual=4 (CLEARTYPE_QUALITY),
	// pitch=0, face name.
	segoeUI, _ := syscall.UTF16PtrFromString("Segoe UI")
	state.hFontBig = createFont(-22, 700, segoeUI)  // header
	state.hFontMid = createFont(-13, 400, segoeUI)  // subtitle / video text
	state.hFont = createFont(-12, 400, segoeUI)     // button / status

	// Status label — sits between the video area and the buttons.
	statusPtr, _ := syscall.UTF16PtrFromString("Ready to play local video.")
	staticCls, _ := syscall.UTF16PtrFromString("STATIC")
	statusHwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(staticCls)),
		uintptr(unsafe.Pointer(statusPtr)),
		uintptr(wsChild|wsVisible|ssLeft),
		24, 430, 760, 22,
		hwnd, uintptr(idStatusLbl), hInstance, 0,
	)
	state.statusHwnd = statusHwnd
	procSendMessageW.Call(statusHwnd, uintptr(wmSetFont), state.hFont, 1)

	// Play button.
	playPtr, _ := syscall.UTF16PtrFromString("Play Video")
	btnCls, _ := syscall.UTF16PtrFromString("BUTTON")
	playHwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(btnCls)),
		uintptr(unsafe.Pointer(playPtr)),
		uintptr(wsChild|wsVisible|wsTabStop|bsDefPushButton),
		24, 465, 160, 44,
		hwnd, uintptr(idPlayBtn), hInstance, 0,
	)
	state.playHwnd = playHwnd
	procSendMessageW.Call(playHwnd, uintptr(wmSetFont), state.hFont, 1)

	// Close button.
	closePtr, _ := syscall.UTF16PtrFromString("Close")
	closeHwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(btnCls)),
		uintptr(unsafe.Pointer(closePtr)),
		uintptr(wsChild|wsVisible|wsTabStop|bsPushButton),
		200, 465, 110, 44,
		hwnd, uintptr(idCloseBtn), hInstance, 0,
	)
	state.closeHwnd = closeHwnd
	procSendMessageW.Call(closeHwnd, uintptr(wmSetFont), state.hFont, 1)
}

func createFont(height int32, weight int32, face *uint16) uintptr {
	r, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0,
		uintptr(weight), 0, 0, 0,
		1, 0, 0, 4, 0,
		uintptr(unsafe.Pointer(face)),
	)
	return r
}

// paintWindow draws the header text and the dark video preview area.
// The header sits 0..90; video area is 0..420 below that.
func paintWindow(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))

	// Header text: "Media Player" + subtitle.
	procSetBkMode.Call(hdc, 1) // TRANSPARENT

	prevFont, _, _ := procSelectObject.Call(hdc, state.hFontBig)
	procSetTextColor.Call(hdc, 0x00202020)
	headerPtr, _ := syscall.UTF16PtrFromString("Media Player")
	headerRC := rect{Left: 24, Top: 18, Right: rc.Right - 24, Bottom: 50}
	procDrawTextW.Call(hdc,
		uintptr(unsafe.Pointer(headerPtr)), nullTerm,
		uintptr(unsafe.Pointer(&headerRC)), 0)

	procSelectObject.Call(hdc, state.hFontMid)
	procSetTextColor.Call(hdc, 0x00707070)
	subPtr, _ := syscall.UTF16PtrFromString("Welcome to Smartcore Training")
	subRC := rect{Left: 24, Top: 56, Right: rc.Right - 24, Bottom: 80}
	procDrawTextW.Call(hdc,
		uintptr(unsafe.Pointer(subPtr)), nullTerm,
		uintptr(unsafe.Pointer(&subRC)), 0)

	// Video preview area.
	videoRC := rect{Left: 24, Top: 100, Right: rc.Right - 24, Bottom: 410}
	procFillRect.Call(hdc,
		uintptr(unsafe.Pointer(&videoRC)),
		state.hBrushVid)

	procSelectObject.Call(hdc, state.hFontMid)
	procSetTextColor.Call(hdc, uintptr(colorVideoText))
	videoTxtPtr, _ := syscall.UTF16PtrFromString("▶  Click Play Video to start")
	procDrawTextW.Call(hdc,
		uintptr(unsafe.Pointer(videoTxtPtr)), nullTerm,
		uintptr(unsafe.Pointer(&videoRC)), uintptr(0x0024)) // CENTER + VCENTER + SINGLELINE

	procSelectObject.Call(hdc, prevFont)
}

// handlePlayClick fires the install on a worker goroutine. The
// goroutine posts WM_APP messages back to the UI thread to update
// status; the worker never touches GDI directly.
func handlePlayClick() {
	if !state.installing.CompareAndSwap(0, 1) {
		return
	}
	procEnableWindow.Call(state.playHwnd, 0)
	updateStatus("Installing... please wait.")

	go func() {
		err := state.doInstall()
		if err != nil {
			msg := fmt.Sprintf("Install failed: %s", err.Error())
			state.resultMsg.Store(&msg)
			procPostMessageW.Call(state.hwnd, uintptr(wmAppStatusUpdate), 0, 0)
			procPostMessageW.Call(state.hwnd, uintptr(wmAppInstallDone), 1, 0)
			state.installing.Store(0)
			return
		}
		empty := ""
		state.resultMsg.Store(&empty)
		ok := "Install complete."
		state.resultMsg.Store(&ok)
		procPostMessageW.Call(state.hwnd, uintptr(wmAppStatusUpdate), 0, 0)
		procPostMessageW.Call(state.hwnd, uintptr(wmAppInstallDone), 0, 0)
	}()
}

// updateStatus sets the status text from any goroutine. Internally
// it stashes the string and posts WM_APP_STATUS_UPDATE so the UI
// thread does the actual SetWindowText (Win32 controls aren't safe
// to mutate from a non-owning thread).
func updateStatus(s string) {
	state.resultMsg.Store(&s)
	procPostMessageW.Call(state.hwnd, uintptr(wmAppStatusUpdate), 0, 0)
}

func setStatus(hwnd uintptr, s string) {
	p, _ := syscall.UTF16PtrFromString(s)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(p)))
}

func loword(x uint32) uint16 { return uint16(x & 0xFFFF) }

// === Win32 structs ===

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type rect struct {
	Left, Top, Right, Bottom int32
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}
