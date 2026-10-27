//go:build windows

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

// wincon.go — the hidden console and the clean stop.
//
// SuperSubtitler stops cleanly on Ctrl+C: the running engine and FFmpeg are
// ended, temp files removed, finished files kept. To trigger exactly that path
// from a window, two things are needed:
//
//  1. A console. A window program has none, and GenerateConsoleCtrlEvent
//     requires one — so one is created at start and hidden at once.
//  2. A process group per run (CREATE_NEW_PROCESS_GROUP), so the signal hits
//     that run and not this window.
//
// The CLI must NOT be started with CREATE_NO_WINDOW: that would give it a
// console of its own, and a Ctrl event from here would never arrive. Go maps
// CTRL_BREAK_EVENT to SIGINT, which the CLI already listens for.
package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	user32               = windows.NewLazySystemDLL("user32.dll")
	procAllocConsole     = kernel32.NewProc("AllocConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
)

const swHide = 0

// setupHiddenConsole gives this window program an invisible console. If one
// already exists ("wails dev" in a terminal), nothing is hidden.
func setupHiddenConsole() {
	created, _, _ := procAllocConsole.Call()
	if created == 0 {
		return
	}
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, swHide)
	}
}

// requestCleanStop sends the Ctrl+Break the CLI treats like Ctrl+C.
func requestCleanStop(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("wincon.go: requestCleanStop: invalid process id %d", pid)
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
		return fmt.Errorf("wincon.go: requestCleanStop: %w", err)
	}
	return nil
}
