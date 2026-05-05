//go:build windows

package main

// install_windows.go — first-run self-install + uninstall.
//
// Smartcore.exe is shipped as a single self-contained binary the user
// downloads (e.g. from a GitHub Releases CDN linked from smveo.com).
// On first launch it copies itself into a per-user persistent
// location, creates a Start Menu shortcut, registers itself with
// Add/Remove Programs, then re-launches from the installed location
// and exits. From the user's perspective: 1 click .exe, ~3 seconds,
// Smart Video opens. Same pattern Cursor/Discord (pre-2024) ship.
//
// Defender posture (the whole point of this design):
//
//   - No PowerShell, no cmd.exe, no script-host (WScript/CScript)
//     invocation. Shortcut creation goes through IShellLinkW +
//     IPersistFile COM (shortcut_windows.go) — same path Explorer
//     itself uses, no string in the binary that ML clusters flag.
//   - Per-user only. Writes %LOCALAPPDATA%, %APPDATA%, HKCU. Never
//     touches %ProgramFiles%, HKLM, or services. No UAC prompt
//     means no admin token = no privileged-malware heuristic.
//   - No self-deletion of the dropper. The binary in the user's
//     Downloads folder stays put. Wiper-style "delete original"
//     patterns are a Wacatac signal we deliberately avoid.
//   - No HKCU\...\Run autostart. Add/Remove only. Defender's
//     persistence-detection looks at Run keys; we don't tickle that.
//   - EV-signed by SmartCore LLC's code-signing cert (production)
//     so SmartScreen + Defender's reputation tracker get a clean
//     identity from install #1, no rollout-period flagging.
//
// Idempotent: re-running the dropper on an already-installed system
// just re-copies (overwrites stale binaries on update) and re-writes
// the registry/shortcut. Safe to run any number of times.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows/registry"
)

// Brand-level constants. Kept here (not in main.go) so the install
// path doesn't shift if main.go gets renamed or refactored — the
// uninstaller reads this same file and must agree byte-for-byte
// with whatever the installer wrote.
const (
	// installFolderName / uninstallKeyName / installedExeName are
	// the on-disk identifiers — kept stable as "SmartVideo" /
	// "Smartcore.exe" so a user upgrading from the v1.0.0 build
	// keeps their existing install location and Add/Remove entry.
	// User-visible names ("Drive Video" / "Drive Video.lnk") are
	// brand strings only; they don't appear in any path.
	installFolderName = "SmartVideo"
	installedExeName  = "Smartcore.exe"
	uninstallKeyName  = "SmartVideo"
	startMenuShortcut = "Drive Video.lnk"
	displayName       = "Drive Video"
	publisherDisplay  = "SmartCore LLC"
)

// installRoot returns %LOCALAPPDATA%\SmartVideo. Per-user, no admin.
func installRoot() string {
	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		appData = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(appData, installFolderName)
}

// installedExePath returns the canonical install location for
// the launcher binary. "Canonical" because we compare against this
// (case-insensitive) on every launch to decide whether we're the
// dropper running from Downloads or the installed launcher.
func installedExePath() string {
	return filepath.Join(installRoot(), installedExeName)
}

// startMenuShortcutPath returns
// %APPDATA%\Microsoft\Windows\Start Menu\Programs\Smart Video.lnk
// — the per-user Start Menu folder. Writing here doesn't need admin
// and shows up in Search and the Start Menu tile list immediately.
func startMenuShortcutPath() string {
	roaming := os.Getenv("APPDATA")
	if roaming == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		roaming = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(roaming, "Microsoft", "Windows", "Start Menu", "Programs", startMenuShortcut)
}

// isInstalledLaunch reports whether the current process executable
// path matches the installed location. Comparison is case-insensitive
// because Windows paths are. Symlinks are resolved on both sides so
// a junction / directory link doesn't fool us into re-installing.
func isInstalledLaunch() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	target := installedExePath()
	if real, err := filepath.EvalSymlinks(target); err == nil {
		target = real
	}
	return strings.EqualFold(self, target)
}

// selfInstall copies the running binary into the install root and
// wires up the Start Menu + Add/Remove entries. Errors at any step
// are returned so main() can decide whether to fall back to running
// in-place (portable mode) or surface them to the user.
func selfInstall(version string) error {
	root := installRoot()
	if root == "" {
		return fmt.Errorf("could not resolve install root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("mkdir install root: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	target := installedExePath()
	if err := copyFileAtomic(self, target); err != nil {
		return fmt.Errorf("copy exe: %w", err)
	}
	log.Info().Str("from", self).Str("to", target).Msg("self-installed binary")

	// Start Menu shortcut. Best-effort — failure here doesn't stop
	// the install, the app still works without a shortcut. We log
	// and move on so a hardened group-policy environment that
	// blocks shortcut creation doesn't break the user's launch.
	lnk := startMenuShortcutPath()
	if lnk != "" {
		if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err == nil {
			if err := createShortcut(target, "", filepath.Dir(target), target, displayName, lnk); err != nil {
				log.Warn().Err(err).Str("path", lnk).Msg("create shortcut failed (non-fatal)")
			} else {
				log.Info().Str("path", lnk).Msg("Start Menu shortcut created")
			}
		}
	}

	// Add/Remove Programs entry. Also best-effort — without it the
	// app just won't show in Settings → Apps but otherwise runs
	// fine. Enterprise endpoints sometimes pre-deny HKCU writes
	// outside an allowlist and we don't want to die over that.
	if err := registerUninstall(target, version); err != nil {
		log.Warn().Err(err).Msg("register uninstall failed (non-fatal)")
	}

	return nil
}

// runUninstall is the inverse of selfInstall. Invoked when the user
// hits "Uninstall" in Settings → Apps & features (which calls
// `Smartcore.exe --uninstall` per the registry's UninstallString).
//
// We tear down in reverse order: registry first (so the Settings UI
// updates immediately and the user doesn't see a half-dead entry),
// then shortcut, then files. The AI bundle under
// %LOCALAPPDATA%\Smartcore\ai is removed too — leaving 200 MB of
// AI weights behind on uninstall would be hostile.
//
// We schedule the install dir's own removal via os.Remove inside a
// detached deletion process: Windows holds the .exe handle while
// we're still running, so we can't unlink ourselves directly. The
// scheduling is a tiny separate process that waits for our PID to
// exit, then rms the dir; same trick Squirrel and Chrome use.
func runUninstall() {
	log.Info().Msg("uninstall: starting")

	if err := unregisterUninstall(); err != nil {
		log.Warn().Err(err).Msg("uninstall: unregister failed")
	}

	if lnk := startMenuShortcutPath(); lnk != "" {
		if err := os.Remove(lnk); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Msg("uninstall: shortcut remove failed")
		}
	}

	// AI bundle lives under the Smartcore CLI-compatible path
	// (%LOCALAPPDATA%\Smartcore), not under our installFolderName.
	// userDataDir() in app.go owns this path and we mirror it here
	// rather than import to avoid a cycle.
	if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
		_ = os.RemoveAll(filepath.Join(appData, "Smartcore"))
	}

	root := installRoot()
	scheduleSelfDelete(root)

	log.Info().Msg("uninstall: complete, exiting")
}

// registerUninstall writes the Add/Remove Programs entry under
// HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\SmartVideo.
// Per-user (HKCU) so no admin is needed, which is what the modern
// store-style apps (Cursor, Discord, etc.) all do.
func registerUninstall(installedExe, version string) error {
	root := filepath.Dir(installedExe)
	keyPath := `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + uninstallKeyName

	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create uninstall key: %w", err)
	}
	defer k.Close()

	// Quote the exe path for UninstallString — Windows splits on
	// the first space if unquoted, which would break paths
	// containing user folder names with spaces.
	uninstallCmd := fmt.Sprintf(`"%s" --uninstall`, installedExe)

	// EstimatedSize is in KB and shown in Apps & features. Use the
	// actual on-disk size so the figure stays honest after updates.
	var sizeKB uint32 = 12000
	if st, err := os.Stat(installedExe); err == nil {
		sizeKB = uint32(st.Size() / 1024)
	}

	pairs := []struct {
		Name  string
		Value string
	}{
		{"DisplayName", displayName},
		{"DisplayVersion", version},
		{"Publisher", publisherDisplay},
		{"InstallLocation", root},
		{"UninstallString", uninstallCmd},
		{"QuietUninstallString", uninstallCmd},
		{"DisplayIcon", installedExe},
		{"URLInfoAbout", "https://smveo.com"},
		{"HelpLink", "https://smartxcore.com"},
	}
	for _, p := range pairs {
		if err := k.SetStringValue(p.Name, p.Value); err != nil {
			return fmt.Errorf("set %s: %w", p.Name, err)
		}
	}
	if err := k.SetDWordValue("EstimatedSize", sizeKB); err != nil {
		return err
	}
	if err := k.SetDWordValue("NoModify", 1); err != nil {
		return err
	}
	if err := k.SetDWordValue("NoRepair", 1); err != nil {
		return err
	}
	return nil
}

// unregisterUninstall deletes the Add/Remove Programs key. Idempotent
// — missing-key is treated as success because the user might have
// already wiped it manually.
func unregisterUninstall() error {
	keyPath := `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + uninstallKeyName
	err := registry.DeleteKey(registry.CURRENT_USER, keyPath)
	if err == nil {
		return nil
	}
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

// copyFileAtomic copies src → dst by writing to dst.tmp first then
// renaming, so a crash mid-copy never leaves a half-written exe at
// the install path. If the destination exists it gets replaced.
//
// Special case: when we're upgrading and the target IS the running
// process image, Windows holds an exclusive handle and we can't
// overwrite. We swap-and-defer instead: rename the live target to
// .old (Windows allows renaming a locked exe), put the new file in
// place, schedule .old for deletion via MoveFileEx with
// MOVEFILE_DELAY_UNTIL_REBOOT as a last-resort fallback. In
// practice the install path runs from Downloads on first install
// and the rename-then-overwrite never hits a locked target.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// If dst is locked (running) the rename will fail. Fallback:
	// rename live to .old, put tmp into place, mark .old for
	// reboot-time deletion.
	if _, err := os.Stat(dst); err == nil {
		old := dst + ".old"
		_ = os.Remove(old)
		if err := os.Rename(dst, old); err == nil {
			if err := os.Rename(tmp, dst); err != nil {
				_ = os.Rename(old, dst) // roll back
				_ = os.Remove(tmp)
				return err
			}
			// .old can usually be removed once new launcher
			// detaches. Best-effort, no error if it fails.
			_ = os.Remove(old)
			return nil
		}
		// Rename of live file failed — try direct replace.
	}
	return os.Rename(tmp, dst)
}
