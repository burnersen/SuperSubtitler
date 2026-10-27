//go:build windows

// windowstate.go — size, place and theme of the window between starts.
// Stored as SuperSubtitlerGUI.window next to the exe; nothing in the registry.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

const (
	windowStateFileName = "SuperSubtitlerGUI.window"

	defaultWindowWidth  = 1120
	defaultWindowHeight = 820
	minWindowWidth      = 880
	minWindowHeight     = 620
	maxWindowExtent     = 20000

	themeDark  = "dark"
	themeLight = "light"
)

type windowState struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Maximised bool   `json:"maximised"`
	Theme     string `json:"theme"`
}

func defaultWindowState() windowState {
	return windowState{Width: defaultWindowWidth, Height: defaultWindowHeight, Theme: themeDark}
}

func normaliseTheme(theme string) string {
	if theme == themeLight {
		return themeLight
	}
	return themeDark
}

func (s windowState) sizeIsSensible() bool {
	return s.Width >= minWindowWidth && s.Height >= minWindowHeight &&
		s.Width <= maxWindowExtent && s.Height <= maxWindowExtent
}

func windowStatePath() string {
	return filepath.Join(guiDir(), windowStateFileName)
}

// loadWindowState returns the remembered state; ok is false when nothing
// usable was remembered (the theme survives even a broken size).
func loadWindowState() (windowState, bool) {
	raw, err := os.ReadFile(windowStatePath())
	if err != nil {
		return defaultWindowState(), false
	}
	var s windowState
	if err := json.Unmarshal(raw, &s); err != nil {
		return defaultWindowState(), false
	}
	s.Theme = normaliseTheme(s.Theme)
	if !s.sizeIsSensible() {
		d := defaultWindowState()
		d.Theme = s.Theme
		return d, false
	}
	return s, true
}

func saveWindowState(s windowState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("windowstate.go: saveWindowState: %w", err)
	}
	return os.WriteFile(windowStatePath(), raw, 0o644)
}

var procMonitorFromRect = user32.NewProc("MonitorFromRect")

type winRect struct{ Left, Top, Right, Bottom int32 }

// rectIsOnAScreen guards against restoring the window onto a monitor that is
// no longer connected, where it would open invisibly.
func rectIsOnAScreen(x, y, w, h int) bool {
	r := winRect{int32(x), int32(y), int32(x + w), int32(y + h)}
	const monitorDefaultToNull = 0
	handle, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), monitorDefaultToNull)
	return handle != 0
}
