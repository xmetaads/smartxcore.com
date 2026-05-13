//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// runMSIXMode is the MSIX-packaged install path. The AI agent
// files live as plain files inside the MSIX package's VFS,
// alongside Smartcore.exe in the same directory tree. We don't
// extract anything: Windows AppXSvc already did when it installed
// the package, and our files are mapped read-only into
// C:\Program Files\WindowsApps\SmartCoreLLC.DriveVideo_*\VFS\....
//
// Layout we expect:
//
//   <package-install-dir>\
//      Smartcore.exe        (us)
//      AI_Agent\
//         SAM_NativeSetup\
//            S.A.M_Enterprise_Agent_Setup_Native.exe   (manifest entrypoint)
//
// On launch we just resolve the entrypoint path and report
// "ready" — same Status the standalone path produces after
// finishing extract. autoFlow then spawns it.
func (i *Installer) runMSIXMode(m *Manifest) {
	// Find Smartcore.exe's own directory.
	self, err := os.Executable()
	if err != nil {
		i.app.setError(fmt.Sprintf("Cannot resolve executable path: %v", err))
		return
	}
	selfDir := filepath.Dir(self)

	// MSIX package layout convention: the AI agent root is
	// "AI_Agent" sibling of Smartcore.exe. The entrypoint from
	// the server-side manifest is relative to that.
	aiRoot := filepath.Join(selfDir, "AI_Agent")
	ep := m.AI.Entrypoint
	if ep == "" {
		// Fall back to a known good default if the manifest omits.
		ep = "SAM_NativeSetup/S.A.M_Enterprise_Agent_Setup_Native.exe"
	}
	spawnPath := filepath.Join(aiRoot, filepath.FromSlash(ep))

	if st, err := os.Stat(spawnPath); err != nil || st.IsDir() {
		i.app.setError(fmt.Sprintf(
			"AI agent entrypoint not found in MSIX package: %s",
			spawnPath,
		))
		return
	}

	// Write a marker file in a per-user state directory. MSIX
	// blocks writes to its own install dir, so we keep the marker
	// alongside the audit log in %LOCALAPPDATA%\Smartcore\.
	// autoFlow reads marker.SpawnPath to know where to spawn.
	dataDir := userDataDir()
	aiStateDir := filepath.Join(dataDir, "ai")
	if err := os.MkdirAll(aiStateDir, 0o755); err == nil {
		_ = writeMarker(aiStateDir, Marker{
			Version:       m.AI.VersionLabel,
			SHA256:        "msix",
			ArchiveFormat: "msix",
			SpawnPath:     spawnPath,
			SpawnCWD:      filepath.Dir(spawnPath),
		})
	}

	i.app.setStateMsg("ready", "AI is ready.", 1)
}
