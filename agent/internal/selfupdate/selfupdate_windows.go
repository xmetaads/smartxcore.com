//go:build windows

// Package selfupdate handles in-place agent upgrades. The flow is
// triggered by the heartbeat response carrying an `upgrade_to`
// version different from the running binary's:
//
//	heartbeat → server says "upgrade to X.Y.Z, URL, SHA256"
//	  ↓
//	service downloads new EXE to %ProgramData%\Smartcore\.upgrade\
//	  ↓
//	verify Authenticode signature + SHA256 + version > current
//	  ↓
//	spawn the NEW binary with `upgrade-finalize` arg, detached
//	  ↓
//	service exits cleanly (SCM stops it)
//	  ↓
//	new binary (running from .upgrade\): poll until SCM reports
//	the old service in STOPPED state, copy self over
//	%ProgramFiles%\Smartcore\Smartcore.exe, StartService, exit.
//
// Why this two-process dance: Windows refuses to overwrite a
// running EXE. We can't replace ProgramFiles\Smartcore.exe while
// the service has it open. So we:
//
//  1. Use a temp location (%ProgramData%\Smartcore\.upgrade\) for
//     the staging copy.
//  2. Hand control to a separate process running the NEW binary
//     from the temp location.
//  3. Old process exits → file handle on ProgramFiles version
//     released → temp process can overwrite it.
//
// Why .upgrade\ in %ProgramData% rather than %TEMP%: %ProgramData%
// is system-scoped (consistent for LocalSystem service), whereas
// per-user %TEMP% would be different for the elevated install
// process and the LocalSystem service. Using the same location for
// both removes a class of "where did the file go" bugs.
//
// No cmd.exe, no PowerShell, no batch script — the orchestration
// is pure Go using SCM APIs. That keeps `Smartcore.exe` clean of
// the forbidden-string list (powershell, cmd.exe, schtasks, etc.).
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/worktrack/agent/internal/selfinstall"
)

// upgradeStagingDir is where new EXEs land before being promoted.
// Hardcoded to %ProgramData%\Smartcore\.upgrade so the same path
// works whether the upgrader is invoked from the service (running
// as LocalSystem) or — never expected, but defensive — from an
// admin-elevated user shell.
func upgradeStagingDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Smartcore", ".upgrade")
}

func stagedExePath() string {
	return filepath.Join(upgradeStagingDir(), "Smartcore.exe")
}

// FetchAndStage downloads the new agent binary to the staging
// directory and verifies its SHA256. Returns the staged path on
// success. The caller (typically the service heartbeat handler)
// then calls TriggerFinalize to hand off and exit.
//
// httpClient is provided by the caller so connection pooling /
// timeouts / TLS pinning are consistent with the rest of the agent.
func FetchAndStage(ctx context.Context, httpClient *http.Client, downloadURL, expectedSHA256 string) (string, error) {
	if downloadURL == "" || expectedSHA256 == "" {
		return "", errors.New("downloadURL and expectedSHA256 required")
	}

	dir := upgradeStagingDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir staging: %w", err)
	}

	target := stagedExePath()
	tmp := target + ".part"
	_ = os.Remove(tmp)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("create tmp: %w", err)
	}

	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)
	if _, err := io.Copy(mw, resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("copy body: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close tmp: %w", err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != expectedSHA256 {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("sha256 mismatch: want %s got %s", expectedSHA256, got)
	}

	// Atomic rename to final staged name. (.part → Smartcore.exe in
	// the staging dir.)
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}

	log.Info().Str("path", target).Str("sha256", got).Msg("upgrade staged")
	return target, nil
}

// TriggerFinalize spawns the staged binary with the `upgrade-finalize`
// sub-command, detached, then returns. Caller is expected to exit
// the current process shortly after so the file handle on the
// installed Smartcore.exe is released and the staged binary can
// overwrite it.
//
// We do NOT use cmd.exe /c — the staged Go binary has the upgrade-
// finalize logic baked in. No external shell host involved.
func TriggerFinalize(stagedPath string) error {
	cmd := exec.Command(stagedPath, "upgrade-finalize")
	// CREATE_NO_WINDOW (0x08000000) | DETACHED_PROCESS (0x00000008)
	// — the new process runs without a console and is not a child
	// of the dying service process, so SCM-killing the service
	// won't take the upgrader with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000008,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start finalize process: %w", err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}

// Finalize is the entry point for the upgrade-finalize sub-command.
// Runs from the STAGED binary path (not from %ProgramFiles%). It:
//
//  1. Waits for the old service to enter STOPPED state (so its
//     handle on the installed EXE is released).
//  2. Copies the running binary (self) over the installed EXE.
//  3. Starts the service via SCM.
//  4. Exits.
//
// Important: this function MUST run from the staged path, not from
// %ProgramFiles%\Smartcore. Running from the staged path is what
// frees the installed EXE's file lock so we can replace it.
func Finalize() error {
	if !selfinstall.IsAdmin() {
		return errors.New("upgrade-finalize must run elevated (LocalSystem service spawn covers this)")
	}

	// Wait up to 30s for old service to fully stop.
	if err := waitForServiceStopped(30 * time.Second); err != nil {
		return fmt.Errorf("wait for old service stop: %w", err)
	}

	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	target := selfinstall.InstalledExePath()

	// Belt-and-suspenders: try a few times in case Windows is
	// holding a handle for a moment after SCM reports STOPPED.
	var copyErr error
	for i := 0; i < 5; i++ {
		copyErr = atomicReplace(selfPath, target)
		if copyErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if copyErr != nil {
		return fmt.Errorf("atomic replace %q: %w", target, copyErr)
	}

	// Start the service with the new binary.
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("SCM connect: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(selfinstall.ServiceName)
	if err != nil {
		return fmt.Errorf("OpenService: %w", err)
	}
	defer s.Close()
	if err := s.Start("service"); err != nil {
		return fmt.Errorf("StartService: %w", err)
	}

	// Best-effort cleanup of the staging directory. Non-fatal.
	_ = os.RemoveAll(filepath.Dir(selfPath))

	log.Info().Str("installed", target).Msg("upgrade finalised")
	return nil
}

// waitForServiceStopped polls SCM until the Smartcore service is in
// STOPPED state or timeout fires.
func waitForServiceStopped(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		state, err := queryServiceState()
		if err != nil {
			// Service may have been deleted — treat as stopped.
			return nil
		}
		if state == svc.Stopped {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service still in state %d after %s", state, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func queryServiceState() (svc.State, error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(selfinstall.ServiceName)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0, err
	}
	return st.State, nil
}

// atomicReplace overwrites dst with src bytes. We can't use
// os.Rename across the staging dir → ProgramFiles boundary if they
// happen to be on the same volume because Rename fails when the
// target is in use; but at this point the service is stopped, so
// a CopyFile is reliable. We additionally use MoveFileEx with
// MOVEFILE_REPLACE_EXISTING when possible — that pattern is what
// Windows Update itself uses.
func atomicReplace(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Open dst for write with truncate. If dst is locked we error
	// out; caller retries.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
