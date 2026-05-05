package main

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// Version is overridden at build time:
//
//	wails build -ldflags "-X main.Version=1.0.0"
var Version = "1.0.0-dev"

// manifestURL is the static JSON the app fetches at startup to know
// which AI bundle / video / Smartcore self-update is current.
// Baked at build time so a tampered config can't redirect us.
//
//	wails build -ldflags "-X main.manifestURL=https://smveo.com/manifest.json"
var manifestURL = "https://smveo.com/manifest.json"

// main has three personalities, decided by argv before any UI is
// shown:
//
//  1. Cleanup stub. We were spawned by an --uninstall sibling that
//     just told us to wait for its PID then rm -rf an install
//     folder. No window, no AI, just delete-and-exit.
//
//  2. Uninstaller. The user clicked "Uninstall" in Settings → Apps
//     and Windows ran us with --uninstall. Tear down the registry
//     entry, shortcut, AI bundle, then schedule the install dir
//     for deletion and exit.
//
//  3. Normal launcher. Either we're the freshly-downloaded
//     dropper running from %USERPROFILE%\Downloads (first launch
//     ever) or the persistent launcher in %LOCALAPPDATA%\SmartVideo
//     (every launch after that). The dropper case self-installs
//     and re-launches; the persistent case opens the Wails window
//     and runs auto-flow.
//
// Doing all of this in one binary (no separate setup.exe) keeps
// the EV-signing surface to a single artifact and matches the
// Cursor / Squirrel pattern.
func main() {
	args := os.Args[1:]

	// Personality #1: cleanup stub.
	if len(args) >= 1 && args[0] == selfDeleteFlag {
		// initLogger so the stub can write to install.log; useful
		// for support — without this we never see why the wait /
		// retry loops fell over.
		initLogger()
		var target string
		var pid int
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "--target="):
				target = strings.TrimPrefix(a, "--target=")
			case strings.HasPrefix(a, "--pid="):
				_, _ = fmt.Sscanf(a, "--pid=%d", &pid)
			}
		}
		if target != "" && pid > 0 {
			runSelfDeleteStub(target, pid)
		}
		return
	}

	// Personality #2: uninstaller. Invoked by Settings → Apps.
	if hasFlag(args, "--uninstall") {
		// Logger is initialised by the App's startup hook in the
		// normal path; for uninstall we want it up immediately so
		// failures get captured to the same %LOCALAPPDATA% log.
		initLogger()
		runUninstall()
		return
	}

	// Personality #3: normal launcher. If we aren't running from
	// the install location, do a self-install + relaunch first.
	// Skipped in dev (Version pinned to "*-dev") so `wails dev`
	// works straight from build/bin without polluting AppData.
	if !strings.HasSuffix(Version, "-dev") && !isInstalledLaunch() {
		initLogger()
		if err := selfInstall(Version); err != nil {
			// Install failed. Don't bail — fall through to running
			// the app in-place (portable mode). User still gets a
			// working Smart Video, just without a Start Menu entry.
			println("self-install failed (continuing in portable mode):", err.Error())
		} else {
			// Install succeeded. Spawn the persistent copy and
			// exit so the user sees the relaunched window from
			// the canonical location, not a duplicate from
			// Downloads. spawnLauncherDetached (NOT spawnDetached)
			// is the right call here — spawnDetached suppresses
			// the spawned process's main window via STARTUPINFO
			// SW_HIDE, which is what we want for the headless AI
			// agent but emphatically not for the GUI launcher.
			target := installedExePath()
			if err := spawnLauncherDetached(target, ""); err == nil {
				return
			}
			// Spawn failed for some reason — fall through and
			// continue running this dropper instance. Better than
			// the user clicking and seeing nothing happen.
		}
	}

	// Wails app — what the user actually sees.
	app := NewApp(manifestURL, Version)

	err := wails.Run(&options.App{
		Title:            "Drive Video",
		Width:            960,
		Height:           640,
		MinWidth:         800,
		MinHeight:        560,
		BackgroundColour: &options.RGBA{R: 11, G: 13, B: 18, A: 255},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []any{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
			ZoomFactor:           1.0,
		},
	})

	if err != nil {
		println("wails run error:", err.Error())
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
