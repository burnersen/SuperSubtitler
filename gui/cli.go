//go:build windows

// cli.go — finding, checking and fetching SuperSubtitler.exe.
//
// The window and the CLI live in the same folder: the CLI keeps FFmpeg,
// Python, the speech models and its two settings files next to itself, and the
// window reads those settings files from there too.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"SuperSubtitlerGUI/core"

	"golang.org/x/sys/windows"
)

const (
	cliFileName      = "SuperSubtitler.exe"
	cliConfigName    = "SuperSubtitler_Config.ini"
	cliCleanerName   = "SuperSubtitler_SRTCleaner.txt"
	latestReleaseAPI = "https://api.github.com/repos/burnersen/SuperSubtitler/releases/latest"
)

// CLIStatus is what the window knows about the CLI next to it.
type CLIStatus struct {
	Found         bool   `json:"found"`
	Path          string `json:"path"`
	Dir           string `json:"dir"`
	Version       string `json:"version"`
	TooOld        bool   `json:"tooOld"`
	MinVersion    string `json:"minVersion"`
	SetupComplete bool   `json:"setupComplete"`
	Problem       string `json:"problem"`
	// BackupVersion is the version of SuperSubtitler.exe.bak when that one is
	// usable while the current one is not (too old, missing, broken) — the
	// window then offers to put it back.
	BackupVersion string `json:"backupVersion"`
}

func guiDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func cliPath() string { return filepath.Join(guiDir(), cliFileName) }

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// probeCLI asks the CLI for its version, and looks at the backup copy when
// the current one is not usable.
func probeCLI() CLIStatus {
	st := CLIStatus{Path: cliPath(), Dir: guiDir(), MinVersion: core.MinCLIVersion}
	if fileExists(st.Path) {
		st.Found = true
		v, err := exeVersion(st.Path)
		switch {
		case err != nil:
			st.Problem = err.Error()
		default:
			st.Version = v
			st.TooOld = !core.AtLeast(v, core.MinCLIVersion)
		}
		st.SetupComplete = setupLooksComplete(st.Dir)
	}
	if !st.Found || st.TooOld || st.Problem != "" {
		if fileExists(cliPath() + ".bak") {
			if v, err := exeVersion(cliPath() + ".bak"); err == nil && core.AtLeast(v, core.MinCLIVersion) {
				st.BackupVersion = v
			}
		}
	}
	return st
}

// exeVersion asks one SuperSubtitler.exe for its version. stdin stays closed
// and the call is time-bounded: an old CLI that does not know -version shows
// its help and would wait for Enter — a closed input ends that wait at once.
func exeVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-version")
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if v, ok := core.ParseVersion(string(out)); ok {
		return v, nil
	}
	if err != nil {
		return "", errors.New("could not start it: " + err.Error())
	}
	return "", errors.New("it did not report a version")
}

// restoreCLIBackup swaps SuperSubtitler.exe.bak back in (the current exe
// becomes the .bak, so the swap can be undone the same way).
func restoreCLIBackup() (CLIStatus, error) {
	bak := cliPath() + ".bak"
	if !fileExists(bak) {
		return probeCLI(), errors.New("there is no SuperSubtitler.exe.bak to restore")
	}
	tmp := cliPath() + ".swap"
	_ = os.Remove(tmp)
	if fileExists(cliPath()) {
		if err := os.Rename(cliPath(), tmp); err != nil {
			return probeCLI(), fmt.Errorf("could not move the current exe aside: %w", err)
		}
	}
	if err := os.Rename(bak, cliPath()); err != nil {
		_ = os.Rename(tmp, cliPath())
		return probeCLI(), fmt.Errorf("could not restore the backup: %w", err)
	}
	if fileExists(tmp) {
		_ = os.Rename(tmp, bak)
	}
	return probeCLI(), nil
}

// setupLooksComplete checks the markers the CLI leaves after its one-time
// setup: FFmpeg, the ready Python runtime and at least one fetched model
// (pinned models mark their folder with .complete, others are listed in
// .ss_models).
func setupLooksComplete(dir string) bool {
	if !fileExists(filepath.Join(dir, "ffmpeg.exe")) || !fileExists(filepath.Join(dir, "ffprobe.exe")) ||
		!fileExists(filepath.Join(dir, "Python", ".ss_ready")) {
		return false
	}
	models := filepath.Join(dir, "whisper", "models")
	if fileExists(filepath.Join(models, ".ss_models")) {
		return true
	}
	done, _ := filepath.Glob(filepath.Join(models, "pinned", "*", ".complete"))
	return len(done) > 0
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// downloadCLI fetches SuperSubtitler.exe from the latest GitHub release and
// puts it next to the window — but only when that release is newer than what
// is there and new enough for this window (core.UpdateDecision). The previous
// exe is kept as .bak, and put back at once if the new one does not work.
func downloadCLI() (CLIStatus, error) {
	before := probeCLI()
	req, err := http.NewRequest(http.MethodGet, latestReleaseAPI, nil)
	if err != nil {
		return before, err
	}
	req.Header.Set("User-Agent", "SuperSubtitlerGUI")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return before, fmt.Errorf("asking GitHub for the latest release failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return before, fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	var rel core.Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return before, fmt.Errorf("reading the release failed: %w", err)
	}
	installed := ""
	if before.Found && before.Problem == "" {
		installed = before.Version
	}
	if ok, why := core.UpdateDecision(installed, rel.Tag); !ok {
		return before, errors.New(why)
	}
	url, ok := rel.AssetURL(cliFileName)
	if !ok {
		return before, fmt.Errorf("release %s has no %s", rel.Tag, cliFileName)
	}

	dl, err := httpClient.Get(url)
	if err != nil {
		return before, fmt.Errorf("download failed: %w", err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		return before, fmt.Errorf("download answered HTTP %d", dl.StatusCode)
	}
	tmp := cliPath() + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return before, err
	}
	if _, err := io.Copy(f, dl.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return before, fmt.Errorf("download failed: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return before, err
	}
	// Check the new exe BEFORE it replaces anything.
	if v, err := exeVersion(tmp); err != nil || !core.AtLeast(v, core.MinCLIVersion) {
		os.Remove(tmp)
		if err != nil {
			return before, fmt.Errorf("the downloaded SuperSubtitler.exe does not work (%v) — your current one was left as it is", err)
		}
		return before, fmt.Errorf("the downloaded SuperSubtitler.exe is %s, too old for this window — your current one was left as it is", v)
	}
	if fileExists(cliPath()) {
		_ = os.Remove(cliPath() + ".bak")
		if err := os.Rename(cliPath(), cliPath()+".bak"); err != nil {
			os.Remove(tmp)
			return before, fmt.Errorf("could not move the old exe aside: %w", err)
		}
	}
	if err := os.Rename(tmp, cliPath()); err != nil {
		_ = os.Rename(cliPath()+".bak", cliPath())
		return probeCLI(), err
	}
	return probeCLI(), nil
}

// checkConfigText lets the CLI judge new settings before they are written:
// its -checkconfig runs the same validation as a normal start, which would
// otherwise reset the whole file on one bad value.
func checkConfigText(text string) error {
	tmp, err := os.CreateTemp("", "ss-config-*.ini")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliPath(), "-checkconfig:"+tmp.Name())
	cmd.Dir = guiDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	msg := string(out)
	if err != nil {
		if len(msg) > 9 && msg[:9] == "invalid: " {
			return errors.New(trimLine(msg[9:]))
		}
		return fmt.Errorf("SuperSubtitler could not check the settings: %v %s", err, trimLine(msg))
	}
	return nil
}

func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
