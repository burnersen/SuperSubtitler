//go:build windows

// SuperSubtitlerGUI — a window for SuperSubtitler.
//
// The window never transcribes anything itself. It starts the unchanged
// SuperSubtitler.exe with -json and follows its events; every decision about a
// file (language, timing, layout) stays in the CLI.
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

// guiVersion is the one place the window's version is written.
const guiVersion = "0.1.0"

// singleInstanceID must never change, or a new build would not find an old
// window that is already open.
const singleInstanceID = "SuperSubtitlerGUI-3b9e6c1d-window"

func startBackground(theme string) *options.RGBA {
	if normaliseTheme(theme) == themeLight {
		return &options.RGBA{R: 242, G: 243, B: 246, A: 1} // --bg of the light theme
	}
	return &options.RGBA{R: 16, G: 18, B: 21, A: 1} // --bg of the dark theme
}

func main() {
	// Without a console of its own there is no clean stop (see wincon.go).
	setupHiddenConsole()

	window, remembered := loadWindowState()
	app := NewApp(window, remembered)

	start := options.Normal
	if window.Maximised {
		start = options.Maximised
	}
	err := wails.Run(&options.App{
		Title:            baseWindowTitle(),
		Width:            window.Width,
		Height:           window.Height,
		MinWidth:         minWindowWidth,
		MinHeight:        minWindowHeight,
		WindowStartState: start,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: startBackground(window.Theme),
		// File drops reach the page through runtime.OnFileDrop (index.html).
		// DisableWebViewDrop must stay unset: it would switch that path off.
		DragAndDrop:   &options.DragAndDrop{EnableFileDrop: true},
		Windows:       &windows.Options{WebviewIsTransparent: false, WindowIsTranslucent: false},
		OnStartup:     app.startup,
		OnBeforeClose: app.beforeClose,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               singleInstanceID,
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		Bind: []any{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
