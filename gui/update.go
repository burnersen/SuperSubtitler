//go:build windows

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

// update.go — one button brings the window and SuperSubtitler.exe to the
// newest release.
//
// A running exe cannot be overwritten on Windows, but it can be renamed: the
// new SuperSubtitlerGUI.exe is downloaded and checked against the release's
// SHA256SUMS.txt, the running one becomes SuperSubtitlerGUI.exe.old, the new
// one takes its name, and the window restarts itself. The next start deletes
// the .old file.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"SuperSubtitlerGUI/core"

	"golang.org/x/sys/windows"
)

const guiFileName = "SuperSubtitlerGUI.exe"

// UpdateResult tells the window what the update did.
type UpdateResult struct {
	Release    string    `json:"release"`
	CLI        CLIStatus `json:"cli"`
	CLIUpdated bool      `json:"cliUpdated"`
	GUIUpdated bool      `json:"guiUpdated"` // a restart puts the new window up
	Message    string    `json:"message"`
}

func selfPath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return filepath.Join(guiDir(), guiFileName)
}

// updateAll updates whatever is older than the newest release: the CLI
// (kept as .bak, checked before it replaces anything) and the window.
func updateAll() (UpdateResult, error) {
	before := probeCLI()
	res := UpdateResult{CLI: before}
	rel, sums, err := latestRelease()
	if err != nil {
		return res, err
	}
	res.Release = rel.Tag
	relVersion, _ := core.ReleaseVersion(rel.Tag)

	var problems []string
	if cli, err := installCLI(rel, sums, before); err == nil {
		res.CLI, res.CLIUpdated = cli, true
	} else if !before.Found || before.Problem != "" || !core.AtLeast(before.Version, relVersion) {
		problems = append(problems, "SuperSubtitler.exe: "+err.Error())
	}

	if core.GUIUpdate(guiVersion, rel.Tag) {
		if _, ok := sums[strings.ToLower(guiFileName)]; !ok {
			problems = append(problems, "the window: the release lists no checksum for "+guiFileName+" — not installed")
		} else if err := replaceSelf(rel, sums); err != nil {
			problems = append(problems, "the window: "+err.Error())
		} else {
			res.GUIUpdated = true
		}
	}

	switch {
	case len(problems) > 0:
		return res, errors.New(strings.Join(problems, "\n"))
	case res.GUIUpdated && res.CLIUpdated:
		res.Message = "Window and SuperSubtitler updated to " + relVersion + " — the window restarts now."
	case res.GUIUpdated:
		res.Message = "Window updated to " + relVersion + " — it restarts now."
	case res.CLIUpdated:
		res.Message = "SuperSubtitler updated to " + relVersion + "."
	default:
		res.Message = "Everything is up to date (" + relVersion + ")."
	}
	return res, nil
}

// replaceSelf swaps the running window's exe for the release's.
func replaceSelf(rel core.Release, sums map[string]string) error {
	self := selfPath()
	tmp := self + ".download"
	if err := downloadAsset(rel, guiFileName, tmp, sums); err != nil {
		return err
	}
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("could not move the running window aside: %w", err)
	}
	if err := os.Rename(tmp, self); err != nil {
		_ = os.Rename(old, self)
		return fmt.Errorf("could not put the new window in place: %w", err)
	}
	return nil
}

// afterUpdateFlag tells a restarted window which process it replaces.
const afterUpdateFlag = "--after-update="

// restartSelf starts the new window directly, no shell in between: the cmd
// and ping of earlier versions flashed a console window on every update
// (DETACHED_PROCESS left ping without a console, so Windows opened one).
// The new window waits for this one to be gone (waitForPreviousWindow), as
// the single-instance lock would otherwise hand the start back to this one.
func restartSelf() error {
	cmd := exec.Command(selfPath(), afterUpdateFlag+strconv.Itoa(os.Getpid()))
	cmd.Dir = guiDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	return cmd.Start()
}

// waitForPreviousWindow lets a window started by restartSelf wait until the
// old one has closed (at most 15 s; it normally takes well under one).
func waitForPreviousWindow(args []string) {
	for _, a := range args {
		v, ok := strings.CutPrefix(a, afterUpdateFlag)
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(v)
		if err != nil || pid <= 0 {
			return
		}
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			return // already gone
		}
		defer windows.CloseHandle(h)
		_, _ = windows.WaitForSingleObject(h, 15000)
		return
	}
}

// cleanupAfterUpdate deletes the previous window's exe; it tells whether one
// was there (the window then says it was updated). The old process may need
// a moment to let go of the file.
func cleanupAfterUpdate() bool {
	old := selfPath() + ".old"
	if !fileExists(old) {
		return false
	}
	go func() {
		for i := 0; i < 20 && fileExists(old); i++ {
			if os.Remove(old) == nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	return true
}

// checkUpdate returns the newest release's version when it is newer than the
// window or SuperSubtitler.exe here, else "".
func checkUpdate() (string, error) {
	rel, _, err := latestRelease()
	if err != nil {
		return "", err
	}
	v, ok := core.ReleaseVersion(rel.Tag)
	if !ok {
		return "", nil
	}
	cli := probeCLI()
	if core.GUIUpdate(guiVersion, rel.Tag) || (cli.Found && cli.Version != "" && !core.AtLeast(cli.Version, v)) {
		return v, nil
	}
	return "", nil
}
