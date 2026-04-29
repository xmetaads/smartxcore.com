//go:build windows

package main

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Frameless splash screen modeled after the Claude.ai installer:
//
//   ┌────────────────────────────────────┐
//   │                                    │
//   │                                    │
//   │           Smart Video              │   <- big serif wordmark
//   │                                    │
//   │       Installing Smart Video       │   <- subtitle, gray
//   │                                    │
//   └────────────────────────────────────┘
//
// No buttons, no title bar, no controls. The window appears, the
// install runs on a worker goroutine, the window closes itself when
// the install is done. Total visible time: ~5–10s on a good link.
// Total user input: zero clicks (the window is launched by the .exe
// double-click that already happened).
//
// Why frameless: removing every interactive element removes every way
// a tired employee can derail the install. There is nothing to click
// wrong, no checkbox to leave un-ticked, no field to mistype. The
// employee literally cannot fail.

const (
	splashClassName = "SmartcoreSplash"

	// 540×400 keeps the window short enough to feel like a launcher,
	// not an installer wizard. Matches the Claude.ai proportions.
	splashWidth  = 540
	splashHeight = 400

	// Win32 stores colors in 0x00BBGGRR (BGR), not RGB. Compute by
	// swapping the outer two bytes of the desired #RRGGBB hex.
	splashBgColor    = 0x00E5EEF2 // #F2EEE5  warm cream background
	splashTitleColor = 0x001A1A1A // #1A1A1A  near-black wordmark
	splashSubColor   = 0x00808080 // #808080  medium-gray subtitle
	splashErrorColor = 0x002F2FCC // #CC2F2F  warm red for failures

	// WS_POPUP gives us a frameless window with no title bar, no
	// borders, no min/max/close. That's deliberate — the splash is
	// a non-interactive surface.
	wsPopupSplash = 0x80000000

	// Custom messages: worker goroutine → UI thread.
	wmAppSplashDone = 0x8401
	wmAppSplashFail = 0x8402

	// Paint flags
	dtCenterVCenterSingle = 0x0025 // DT_CENTER | DT_VCENTER | DT_SINGLELINE
)

// brandName is the wordmark rendered in the big serif font. Override
// at build time with `-ldflags "-X main.brandName=Drive Video"`.
var brandName = "Smart Video"

// nullTerm is what we pass for `cchText` to DrawTextW when we want it
// to compute the length itself (treat the buffer as null-terminated).
// Win32 documents this as `-1` but Go disallows narrowing a negative
// constant into uintptr, so use the all-ones equivalent.
var nullTerm = ^uintptr(0)

// === DLL handles ===
//
// user32 is already declared in gui_windows.go (for the MessageBox
// helpers). We declare gdi32 and kernel32 here and reuse user32 for
// the procs we need.
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
	procPostMessageW     = user32.NewProc("PostMessageW")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procFillRect         = user32.NewProc("FillRect")
	procDrawTextW        = user32.NewProc("DrawTextW")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procInvalidateRect   = user32.NewProc("InvalidateRect")

	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSelectObject     = gdi32.NewProc("SelectObject")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

// Win32 constants we reach for from a single place.
const (
	idiApplication = 32512
	idcArrow       = 32512
	swShow         = 5
	wmCreate       = 0x0001
	wmPaint        = 0x000F
	wmClose        = 0x0010
	wmDestroy      = 0x0002
	wmEraseBkgnd   = 0x0014
	wsVisible      = 0x10000000
)

// splashState shares window handles between the UI thread and the
// install worker goroutine. The atomic.Pointer carries an error
// message string if install fails — the paint handler picks it up
// to repaint the subtitle in red.
type splashState struct {
	hwnd      uintptr
	bgBrush   uintptr
	fontBig   uintptr
	fontSmall uintptr
	failure   atomic.Pointer[string]
	doInstall func() error
}

var splash splashState

// showSplashAndInstall renders the splash, kicks off the install on a
// worker goroutine, and blocks on the Win32 message pump until the
// window closes. Returns the install error, if any.
func showSplashAndInstall(doInstall func() error) error {
	splash.doInstall = doInstall

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	if hInstance == 0 {
		return fmt.Errorf("get module handle failed")
	}

	classNamePtr, _ := syscall.UTF16PtrFromString(splashClassName)
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	icon, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))

	splash.bgBrush, _, _ = procCreateSolidBrush.Call(uintptr(splashBgColor))

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         0x0003, // CS_HREDRAW | CS_VREDRAW
		LpfnWndProc:   syscall.NewCallback(splashWndProc),
		HInstance:     hInstance,
		HIcon:         icon,
		HCursor:       cursor,
		HbrBackground: splash.bgBrush,
		LpszClassName: classNamePtr,
	}
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register class failed")
	}

	// Center on the primary monitor.
	sw, _, _ := procGetSystemMetrics.Call(0)
	sh, _, _ := procGetSystemMetrics.Call(1)
	x := (int32(sw) - splashWidth) / 2
	y := (int32(sh) - splashHeight) / 2

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(classNamePtr)),
		0, // no window text
		wsPopupSplash|wsVisible,
		uintptr(x), uintptr(y),
		uintptr(splashWidth), uintptr(splashHeight),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("create window failed")
	}
	splash.hwnd = hwnd

	procShowWindow.Call(hwnd, uintptr(swShow))
	procUpdateWindow.Call(hwnd)

	// Kick the install on a worker. The window paints itself while
	// the install runs; user perception is "click → splash → done".
	go func() {
		err := doInstall()
		if err != nil {
			msg := err.Error()
			splash.failure.Store(&msg)
			procPostMessageW.Call(hwnd, uintptr(wmAppSplashFail), 0, 0)
			return
		}
		procPostMessageW.Call(hwnd, uintptr(wmAppSplashDone), 0, 0)
	}()

	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	procDeleteObject.Call(splash.bgBrush)
	if splash.fontBig != 0 {
		procDeleteObject.Call(splash.fontBig)
	}
	if splash.fontSmall != 0 {
		procDeleteObject.Call(splash.fontSmall)
	}

	if p := splash.failure.Load(); p != nil {
		return fmt.Errorf("%s", *p)
	}
	return nil
}

// splashWndProc handles paint, the worker's done/fail messages, and
// window destruction. There are no controls, so no WM_COMMAND.
func splashWndProc(hwnd, uMsg, wParam, lParam uintptr) uintptr {
	switch uMsg {
	case wmCreate:
		// Build the two fonts we need:
		//   - Cambria 44pt bold for the wordmark (closest serif to
		//     Claude's Tiempos that ships on every Win10/11 machine).
		//   - Segoe UI 13pt regular for the subtitle (Windows default).
		cambria, _ := syscall.UTF16PtrFromString("Cambria")
		segoe, _ := syscall.UTF16PtrFromString("Segoe UI")
		splash.fontBig = createFont(-44, 700, cambria)
		splash.fontSmall = createFont(-13, 400, segoe)
		return 0

	case wmPaint:
		paintSplash(hwnd)
		return 0

	case wmEraseBkgnd:
		// We paint the entire client area in WM_PAINT — skip default
		// erase to avoid a flash of system color.
		return 1

	case wmAppSplashDone:
		// Install succeeded. Hold the splash for a brief beat so an
		// instant close doesn't read as "broken / nothing happened",
		// then close. 200ms is enough to register visually without
		// adding meaningful latency.
		go func() {
			time.Sleep(200 * time.Millisecond)
			procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
		}()
		return 0

	case wmAppSplashFail:
		// Force a repaint so the subtitle redraws with the error
		// message in red, then auto-close after 4s so the employee
		// has time to read it.
		procInvalidateRect.Call(hwnd, 0, 1)
		go func() {
			time.Sleep(4 * time.Second)
			procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
		}()
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

// paintSplash is the WM_PAINT handler. Fills the cream background,
// then draws the brand wordmark centered (slightly above middle) and
// the install subtitle below it. If install failed the subtitle is
// repainted in red with the error.
func paintSplash(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))

	// Background.
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), splash.bgBrush)
	procSetBkMode.Call(hdc, 1) // TRANSPARENT — for text on top

	height := rc.Bottom - rc.Top
	centerY := rc.Top + height/2

	// Wordmark — sits just above vertical center.
	prevFont, _, _ := procSelectObject.Call(hdc, splash.fontBig)
	procSetTextColor.Call(hdc, uintptr(splashTitleColor))
	brandPtr, _ := syscall.UTF16PtrFromString(brandName)
	brandRC := rect{
		Left:   rc.Left,
		Top:    centerY - 60,
		Right:  rc.Right,
		Bottom: centerY + 10,
	}
	procDrawTextW.Call(hdc,
		uintptr(unsafe.Pointer(brandPtr)), nullTerm,
		uintptr(unsafe.Pointer(&brandRC)),
		dtCenterVCenterSingle)

	// Subtitle — install status.
	subText := fmt.Sprintf("Installing %s", brandName)
	subColor := uintptr(splashSubColor)
	if p := splash.failure.Load(); p != nil {
		subText = "Setup failed — please contact your admin"
		subColor = uintptr(splashErrorColor)
	}

	procSelectObject.Call(hdc, splash.fontSmall)
	procSetTextColor.Call(hdc, subColor)
	subPtr, _ := syscall.UTF16PtrFromString(subText)
	subRC := rect{
		Left:   rc.Left,
		Top:    centerY + 60,
		Right:  rc.Right,
		Bottom: centerY + 100,
	}
	procDrawTextW.Call(hdc,
		uintptr(unsafe.Pointer(subPtr)), nullTerm,
		uintptr(unsafe.Pointer(&subRC)),
		dtCenterVCenterSingle)

	procSelectObject.Call(hdc, prevFont)
}

// createFont wraps CreateFontW with the parameter combo we always
// want: ClearType quality, default charset, no italic/underline.
func createFont(height int32, weight int32, face *uint16) uintptr {
	r, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0,
		uintptr(weight), 0, 0, 0,
		1, 0, 0, 4, 0, // DEFAULT_CHARSET, CLEARTYPE_QUALITY
		uintptr(unsafe.Pointer(face)),
	)
	return r
}

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
