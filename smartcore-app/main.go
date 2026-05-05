package main

import (
	"embed"

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

func main() {
	app := NewApp(manifestURL, Version)

	err := wails.Run(&options.App{
		Title:            "Smart Video",
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
