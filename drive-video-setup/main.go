//go:build windows

// drive-video-setup -- Drive Video bootstrapper.
//
// This is the small EXE the user downloads from smveo.com. It is the
// equivalent of Anthropic's win32-bootstrapper for Claude Setup.exe.
// The bootstrapper does NOT contain the application itself; it
// fetches a release manifest from api.smveo.com, downloads the
// signed Drive Video.msix from the CDN, and asks Windows to install
// the MSIX via the WinRT PackageManager.AddPackageAsync API.
//
// Once the MSIX is installed, Windows handles every subsequent
// concern (Start-Menu shortcut, uninstall, auto-update via
// AppInstaller.xml, sandbox isolation). The bootstrapper exits and
// is never needed again on the same machine for the same user
// unless the user re-runs it.
//
// Why a stub + MSIX instead of one fat EXE:
//
//   - Defender ML treats MSIX install path as a first-class trust
//     route. Flag rate after EV-sign drops to ~0.5% versus the 3-7%
//     for self-extracting EXEs.
//
//   - AppInstaller.xml auto-update means users never see SmartScreen
//     warnings on subsequent versions. Reputation lives on the
//     publisher cert + the appinstaller URI, not the per-version
//     binary hash.
//
//   - The download is small (~6-8 MB), so users on slow networks
//     don't time out before seeing progress.
//
// Build: cd drive-video-setup && powershell -File build.ps1
// Signed afterwards by the user with signtool against their EV cert.
//
// Compatible with Windows 10 1709 (build 16299) and newer.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

// Build-time-injected constants. The build script bakes these in via
// -ldflags so the same source can target staging or production
// environments without code changes.
var (
	// Version is the bootstrapper's own version string. Shown in
	// the install dialog and the Add/Remove entry. Independent of
	// the Drive Video MSIX version (the bootstrapper itself rarely
	// changes; the MSIX it downloads can).
	Version = "1.0.0-dev"

	// ManifestURL is the JSON API endpoint that returns the
	// current Drive Video MSIX url + sha + version. The
	// bootstrapper polls this once on launch.
	//
	// Production:  https://api.smveo.com/desktop/win32
	// Staging:     https://staging.smveo.com/desktop/win32
	ManifestURL = "https://api.smveo.com/desktop/win32"
)

func main() {
	// Set up minimal logging to %LOCALAPPDATA%\DriveVideoSetup\setup.log
	// so support has something to look at if install fails. Errors
	// surface in the UI; this log captures the full trace.
	initLogger()

	// CLI flags. The bootstrapper supports a few non-interactive
	// modes for IT-admin script deployments and CI testing.
	var (
		silent  = flag.Bool("silent", false, "Install without UI (Intune / SCCM mode)")
		dryRun  = flag.Bool("dry-run", false, "Fetch manifest and verify but don't install")
		showVer = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("Drive Video Setup %s\n", Version)
		fmt.Printf("Manifest URL:  %s\n", ManifestURL)
		os.Exit(0)
	}

	log.Printf("Drive Video Setup %s starting", Version)
	log.Printf("Manifest URL: %s", ManifestURL)

	// Run the bootstrapper. UI mode shows a native Win32 progress
	// dialog; silent mode runs the same pipeline but writes only to
	// stderr + setup.log.
	exitCode := runBootstrap(*silent, *dryRun)
	os.Exit(exitCode)
}

// runBootstrap is the top-level orchestrator. Returns the process
// exit code (0 = success, 1+ = failure with reason logged).
func runBootstrap(silent, dryRun bool) int {
	ctx, cancel := newBootstrapContext()
	defer cancel()

	ui := newUI(silent)
	defer ui.Close()

	ui.Status("Checking Drive Video updates…")

	// Step 1: detect proxy (corporate networks often require this).
	proxy := detectProxy()
	if proxy != "" {
		log.Printf("using proxy: %s", proxy)
	}

	// Step 2: fetch the release manifest.
	man, err := fetchManifest(ctx, ManifestURL, proxy)
	if err != nil {
		ui.Error(fmt.Sprintf("Could not check for updates: %v", err))
		log.Printf("manifest fetch failed: %v", err)
		return 2
	}
	log.Printf("manifest: version=%s msix=%s size=%d", man.Version, man.MsixURL, man.MsixSize)

	// Step 3: check if this exact package is already installed.
	if installed, _ := isPackageInstalled(man.PackageFamilyName); installed {
		log.Printf("package %s already installed, launching", man.PackageFamilyName)
		ui.Status("Drive Video is already installed. Launching…")
		_ = activatePackage(man.PackageFamilyName)
		return 0
	}

	// Step 4: download the MSIX.
	ui.Status("Downloading Drive Video…")
	msixPath, err := downloadMsix(ctx, man, proxy, func(read, total int64) {
		ui.Progress(read, total)
	})
	if err != nil {
		ui.Error(fmt.Sprintf("Download failed: %v", err))
		log.Printf("download failed: %v", err)
		return 3
	}
	log.Printf("MSIX downloaded to %s", msixPath)

	// Step 5: verify SHA256 against the manifest.
	ui.Status("Verifying integrity…")
	if err := verifyMsix(msixPath, man.MsixSHA256); err != nil {
		ui.Error(fmt.Sprintf("Integrity check failed: %v", err))
		log.Printf("verify failed: %v", err)
		return 4
	}

	if dryRun {
		ui.Status("Dry run complete (verified, not installed).")
		log.Print("dry-run mode, exiting without install")
		return 0
	}

	// Step 6: install via WinRT PackageManager.AddPackageAsync.
	ui.Status("Installing Drive Video…")
	if err := installMsix(ctx, msixPath, func(pct int) {
		ui.Progress(int64(pct), 100)
	}); err != nil {
		ui.Error(fmt.Sprintf("Install failed: %v", err))
		log.Printf("install failed: %v", err)
		return 5
	}

	// Step 7: clean up the temp MSIX (Windows has already copied
	// what it needs into WindowsApps; the source file is no longer
	// needed). Best-effort.
	_ = os.Remove(msixPath)

	// Step 8: activate the freshly-installed package and exit.
	ui.Status("Drive Video is ready.")
	log.Printf("activating package %s", man.PackageFamilyName)
	if err := activatePackage(man.PackageFamilyName); err != nil {
		// Activation failed but install succeeded - user can still
		// launch from Start Menu. Surface as warning not error.
		log.Printf("activation failed (install OK): %v", err)
	}

	ui.Done()
	return 0
}
