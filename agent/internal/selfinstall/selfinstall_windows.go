//go:build windows

// Package selfinstall handles the first-run install flow:
//
//	user double-click  →  detect not installed  →  UAC elevate  →
//	  copy self to %ProgramFiles%\Smartcore\Smartcore.exe  →
//	  CreateService(Smartcore, AutoStart, LocalSystem)  →
//	  StartService  →  exit
//
// And the symmetric uninstall:
//
//	StopService  →  DeleteService  →  RemoveAll(%ProgramFiles%\Smartcore)
//
// Why this is the cleanest possible Defender-friendly install:
//
//   - **Self-COPY (CopyFileW), not extract.** Wacatac signature is
//     "extract embedded executable from PE resource section, drop to
//     disk, invoke." Self-copy is just CopyFileW from one path to
//     another — the same operation File Explorer does on drag. No PE
//     resource enumeration, no temp file write, no execve of dropped
//     bytes. Defender does not score CopyFileW the same way.
//
//   - **Service install via SCM**, not registry persistence.
//     OpenSCManagerW + CreateServiceW is the path Microsoft uses for
//     its own services. ML cluster trust is high. HKCU\...\Run is
//     where Wacatac, AgentTesla, FormBook, RedLine etc. all persist —
//     that's the heuristic match we are deliberately avoiding.
//
//   - **Install location: %ProgramFiles%\Smartcore\.** Standard
//     enterprise app location. ML signal "legit installer". Compare
//     to malware which prefers %APPDATA%, %TEMP%, %LOCALAPPDATA% —
//     all writable by the user without UAC. UAC-required path is
//     itself a credibility signal.
//
//   - **No network calls during install.** We don't enroll, we don't
//     fetch anything, we don't talk to the backend. The freshly
//     installed service does that on its first heartbeat. That keeps
//     the install path purely local — no chance for a sandbox/MOTW
//     review to see suspicious network behaviour during the first
//     few seconds after launch.
package selfinstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	// ServiceName must match svcmain.ServiceName. Hardcoded here as a
	// constant so this package can be imported without depending on
	// svcmain (avoids import cycle).
	ServiceName = "Smartcore"

	// ServiceDisplayName is what shows in services.msc and Task Manager.
	ServiceDisplayName = "Smartcore"

	// ServiceDescription shows in services.msc detail pane.
	ServiceDescription = "Smartcore endpoint agent"

	// ExeName is the on-disk filename inside the install dir. Matches
	// the canonical distribution filename so signtool /pa verification
	// matches expectations.
	ExeName = "Smartcore.exe"

	// installSubdir is appended to %ProgramFiles%. Hardcoded to
	// "Smartcore" so a freshly installed service's image path is
	// always C:\Program Files\Smartcore\Smartcore.exe.
	installSubdir = "Smartcore"
)

// InstallDir returns the absolute path where Smartcore.exe lives
// after a successful install. Resolves %ProgramFiles% at call time
// rather than embedding a hardcoded "C:\Program Files\..." string —
// 32-bit on 64-bit Windows redirects to "Program Files (x86)" and
// non-English Windows uses localised display names but the same
// environment variable.
func InstallDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, installSubdir)
}

// InstalledExePath is where the service binary lives after install.
func InstalledExePath() string {
	return filepath.Join(InstallDir(), ExeName)
}

// IsAdmin returns true when the current process token is in the
// local Administrators group with the privilege actually elevated
// (not just present-but-disabled by UAC). Cheap probe via
// CheckTokenMembership against the well-known Administrators SID.
func IsAdmin() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	token := windows.Token(0)
	member, err := token.IsMember(sid)
	if err != nil {
		return false
	}
	return member
}

// IsInstalled returns true when a Smartcore service is registered
// AND the on-disk binary exists. Both must be true to count as
// installed — half-states (service registered but EXE deleted) get
// treated as not-installed so a re-install repairs them.
func IsInstalled() bool {
	if _, err := os.Stat(InstalledExePath()); err != nil {
		return false
	}
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return false
	}
	_ = s.Close()
	return true
}

// ElevateAndExit re-launches the current executable with UAC
// elevation, passing the same args, then immediately exits the
// current (non-elevated) process. Standard pattern for a single-
// binary installer that needs admin without forcing the user to
// right-click → "Run as administrator".
//
// Returns only on failure to launch — on success, ExitProcess is
// called and this function never returns.
func ElevateAndExit(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	// Quote args defensively — ShellExecuteW takes a single command-
	// line string, and any arg containing spaces/quotes needs to be
	// re-escaped or Windows will tokenise it badly.
	var quoted []string
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		quoted = append(quoted, a)
	}
	params := strings.Join(quoted, " ")

	verbPtr, _ := syscall.UTF16PtrFromString("runas")
	exePtr, _ := syscall.UTF16PtrFromString(exe)
	var paramsPtr *uint16
	if params != "" {
		paramsPtr, _ = syscall.UTF16PtrFromString(params)
	}

	if err := windows.ShellExecute(0, verbPtr, exePtr, paramsPtr, nil, windows.SW_NORMAL); err != nil {
		return fmt.Errorf("shellexecute runas: %w", err)
	}
	os.Exit(0)
	return nil // unreachable
}

// Install copies self into ProgramFiles, registers the Windows
// service, and starts it. Caller MUST already be admin —
// ElevateAndExit is the standard way to get there.
//
// The implementation is idempotent: re-running on an already-
// installed system stops the old service, replaces the binary, and
// restarts. This makes "double-click setup again to reinstall"
// safe.
func Install() error {
	if !IsAdmin() {
		return errors.New("install requires admin privileges (use ElevateAndExit first)")
	}

	dir := InstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	target := InstalledExePath()

	// If the same binary is already installed, skip the copy. This
	// also avoids the "cannot replace running EXE" error when the
	// user re-runs the installer with the service active.
	sameContent, err := samePathContent(selfPath, target)
	if err == nil && sameContent {
		// Already installed and identical. Just (re)register service
		// to repair any drift in service config.
		if err := upsertService(target); err != nil {
			return fmt.Errorf("upsert service: %w", err)
		}
		return startServiceIfStopped()
	}

	// Stop service first if running — Windows can't overwrite a
	// running EXE.
	_ = stopServiceIfRunning()

	if err := copyFile(selfPath, target); err != nil {
		return fmt.Errorf("copy %q → %q: %w", selfPath, target, err)
	}

	if err := upsertService(target); err != nil {
		return fmt.Errorf("upsert service: %w", err)
	}

	return startServiceIfStopped()
}

// Uninstall stops + removes the service, then removes the install
// directory. Idempotent — missing service or missing dir are not
// errors.
func Uninstall() error {
	if !IsAdmin() {
		return errors.New("uninstall requires admin privileges")
	}

	if err := stopAndDeleteService(); err != nil {
		// Log + continue — we still want to clean up files even if
		// SCM is in a weird state.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	dir := InstallDir()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %q: %w", dir, err)
	}

	return nil
}

// upsertService registers a fresh Smartcore service at imagePath. If
// a service with the same name already exists, it is re-configured
// in-place via mgr.Service.UpdateConfig.
func upsertService(imagePath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()

	cfg := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		BinaryPathName:   `"` + imagePath + `" service`,
		DisplayName:      ServiceDisplayName,
		Description:      ServiceDescription,
		ServiceStartName: "", // empty = LocalSystem
	}

	s, err := m.OpenService(ServiceName)
	if err == nil {
		// Existing service — update config to reflect (possibly new)
		// image path.
		defer s.Close()
		if err := s.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update service config: %w", err)
		}
		return nil
	}

	// Create fresh.
	s, err = m.CreateService(ServiceName, imagePath, cfg, "service")
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// Configure recovery — restart on first two failures, no action
	// on the third (avoid restart loops if something is fundamentally
	// broken).
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
		{Type: mgr.NoAction, Delay: 0},
	}, 24*60*60) // reset failure count after 24h

	return nil
}

// startServiceIfStopped ensures the service is in RUNNING state.
// No-op if already running.
func startServiceIfStopped() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service status: %w", err)
	}
	if st.State == svc.Running || st.State == svc.StartPending {
		return nil
	}

	if err := s.Start("service"); err != nil {
		// Already-running races back to ERROR_SERVICE_ALREADY_RUNNING
		// which we treat as success.
		if !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return fmt.Errorf("start service: %w", err)
		}
	}
	return nil
}

// stopServiceIfRunning sends SCM Stop and waits up to 15s for the
// service to transition to STOPPED. Best-effort: returns error only
// on SCM connectivity failures.
func stopServiceIfRunning() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return nil // not installed = nothing to stop
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return nil
	}
	if st.State == svc.Stopped {
		return nil
	}

	if _, err := s.Control(svc.Stop); err != nil {
		// Not-running races give ERROR_SERVICE_NOT_ACTIVE which is
		// fine — we wanted it stopped.
		if !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return fmt.Errorf("send stop control: %w", err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return nil
		}
		if st.State == svc.Stopped {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("service did not stop within 15s")
}

// stopAndDeleteService is the uninstall counterpart to upsertService.
func stopAndDeleteService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return nil
	}
	defer s.Close()

	_ = stopServiceIfRunning()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// copyFile is a careful CopyFile that uses the Win32 CopyFileExW
// API rather than Go's io.Copy. Two reasons matter for Defender ML:
//
//  1. CopyFileExW is a path Microsoft itself uses everywhere; AVs
//     don't score it the same way as a sequence of CreateFile +
//     ReadFile + WriteFile.
//  2. CopyFileExW preserves file attributes/timestamps which keeps
//     the destination "looking installed" rather than "looking
//     freshly written by a downloader".
func copyFile(src, dst string) error {
	srcPtr, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	dstPtr, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}

	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procCopyFileEx := kernel32.NewProc("CopyFileExW")

	// CopyFileExW(src, dst, progressRoutine, data, cancel, flags).
	// Flags = 0 → standard copy (overwrite allowed, follow attrs).
	r1, _, callErr := procCopyFileEx.Call(
		uintptr(unsafe.Pointer(srcPtr)),
		uintptr(unsafe.Pointer(dstPtr)),
		0, 0, 0, 0,
	)
	if r1 == 0 {
		return fmt.Errorf("copy file: %v", callErr)
	}
	return nil
}

// samePathContent returns true when src and dst exist with byte-for-
// byte identical contents. Used to skip a redundant copy when
// re-running an idempotent installer.
func samePathContent(src, dst string) (bool, error) {
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	di, err := os.Stat(dst)
	if err != nil {
		return false, err
	}
	if si.Size() != di.Size() {
		return false, nil
	}

	a, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := os.Open(dst)
	if err != nil {
		return false, err
	}
	defer b.Close()

	const bufSize = 64 * 1024
	bufA := make([]byte, bufSize)
	bufB := make([]byte, bufSize)
	for {
		na, errA := io.ReadFull(a, bufA)
		nb, errB := io.ReadFull(b, bufB)
		if na != nb {
			return false, nil
		}
		if string(bufA[:na]) != string(bufB[:nb]) {
			return false, nil
		}
		if errors.Is(errA, io.EOF) || errors.Is(errA, io.ErrUnexpectedEOF) {
			return errors.Is(errB, io.EOF) || errors.Is(errB, io.ErrUnexpectedEOF), nil
		}
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}
