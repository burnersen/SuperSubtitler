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

// probeCLI asks the CLI for its version. stdin stays closed and the call is
// time-bounded: an old CLI that does not know -version shows its help and
// would wait for Enter — a closed input ends that wait at once.
func probeCLI() CLIStatus {
	st := CLIStatus{Path: cliPath(), Dir: guiDir(), MinVersion: core.MinCLIVersion}
	if !fileExists(st.Path) {
		return st
	}
	st.Found = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, st.Path, "-version")
	cmd.Dir = st.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	v, ok := core.ParseVersion(string(out))
	switch {
	case ok:
		st.Version = v
		st.TooOld = !core.AtLeast(v, core.MinCLIVersion)
	case err != nil:
		st.Problem = "could not start it: " + err.Error()
	default:
		st.Problem = "it did not report a version"
	}
	st.SetupComplete = setupLooksComplete(st.Dir)
	return st
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
// puts it next to the window. An existing exe is kept as .bak, so there is
// always a way back.
func downloadCLI() (CLIStatus, error) {
	req, err := http.NewRequest(http.MethodGet, latestReleaseAPI, nil)
	if err != nil {
		return CLIStatus{}, err
	}
	req.Header.Set("User-Agent", "SuperSubtitlerGUI")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return CLIStatus{}, fmt.Errorf("asking GitHub for the latest release failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CLIStatus{}, fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	var rel core.Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return CLIStatus{}, fmt.Errorf("reading the release failed: %w", err)
	}
	url, ok := rel.AssetURL(cliFileName)
	if !ok {
		return CLIStatus{}, fmt.Errorf("release %s has no %s", rel.Tag, cliFileName)
	}

	dl, err := httpClient.Get(url)
	if err != nil {
		return CLIStatus{}, fmt.Errorf("download failed: %w", err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		return CLIStatus{}, fmt.Errorf("download answered HTTP %d", dl.StatusCode)
	}
	tmp := cliPath() + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return CLIStatus{}, err
	}
	if _, err := io.Copy(f, dl.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return CLIStatus{}, fmt.Errorf("download failed: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return CLIStatus{}, err
	}
	if fileExists(cliPath()) {
		_ = os.Remove(cliPath() + ".bak")
		if err := os.Rename(cliPath(), cliPath()+".bak"); err != nil {
			os.Remove(tmp)
			return CLIStatus{}, fmt.Errorf("could not move the old exe aside: %w", err)
		}
	}
	if err := os.Rename(tmp, cliPath()); err != nil {
		return CLIStatus{}, err
	}
	st := probeCLI()
	if st.TooOld {
		return st, errors.New("the latest release (" + st.Version + ") is older than " + core.MinCLIVersion +
			", which this window needs — a newer SuperSubtitler release is required")
	}
	return st, nil
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
