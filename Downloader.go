//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pterm/pterm"
)

// httpClient bounds connect/handshake/response time on every download, so a
// stalled connection can never hang the tool forever behind a spinner. The
// overall cap is generous enough for the large CUDA wheels and models on slow
// links (the engine/model fetches go through pip, not this client).
var httpClient = &http.Client{
	Timeout: 30 * time.Minute,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// embeddedDriver is transcribe.py, written into Python\ on first run and kept
// in sync with the installed engine version.
//
//go:embed transcribe.py
var embeddedDriver []byte

const getPipURL = "https://bootstrap.pypa.io/get-pip.py"

// cudaWheels ship the CUDA cuBLAS/cuDNN DLLs so the GPU path works without a
// system-wide CUDA toolkit. Used only with a custom engine_spec; the default
// installs the pinned versions from runtime.json instead.
var cudaWheels = []string{"nvidia-cublas-cu12", "nvidia-cudnn-cu12"}

func pythonEmbedURL(version string) string {
	return fmt.Sprintf("https://www.python.org/ftp/python/%s/python-%s-embed-amd64.zip", version, version)
}

// pthName turns "3.12.10" into "python312._pth" (the embeddable's path file).
func pthName(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return "python3._pth"
	}
	return "python" + parts[0] + parts[1] + "._pth"
}

// ----------------------------------------------------------------------------
// FFmpeg
// ----------------------------------------------------------------------------

// ffmpegMarkerName records which pinned FFmpeg build is next to the EXE. An
// FFmpeg without it (an older auto-download, or one put there by hand) is
// replaced by the pinned build, so every install runs the same FFmpeg.
const ffmpegMarkerName = ".ss_ffmpeg"

func ffmpegMarker() string { return pins.FFmpeg.Version + " " + pins.FFmpeg.SHA256 }

// ffmpegIsPinned reports whether ffmpeg.exe/ffprobe.exe in dir are the pinned build.
func ffmpegIsPinned(dir string) bool {
	return fileExists(filepath.Join(dir, "ffmpeg.exe")) && fileExists(filepath.Join(dir, "ffprobe.exe")) &&
		markerMatches(filepath.Join(dir, ffmpegMarkerName), ffmpegMarker())
}

// downloadFFmpeg fetches the pinned FFmpeg build — this project's own lean
// LGPL build from the mirror (.github/scripts/build_ffmpeg.sh), checked
// against its SHA-256 — and extracts only ffmpeg.exe and ffprobe.exe.
func downloadFFmpeg(ctx context.Context, targetDir string) error {
	label := "Downloading FFmpeg " + pins.FFmpeg.Version
	spinner, _ := pterm.DefaultSpinner.WithText(label + "...").Start()
	currentSpinner.Store(spinner)
	defer currentSpinner.Store(nil)
	stop := func() {
		if spinner != nil {
			_ = spinner.Stop()
		}
	}
	tmp, err := os.CreateTemp("", "ffmpeg-*.zip")
	if err != nil {
		stop()
		return fmt.Errorf("Downloader.go: downloadFFmpeg (temp): %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	f := pins.FFmpeg
	if err := downloadPinned(ctx, "ffmpeg", label, tmp.Name(), f.Size, f.SHA256,
		pins.sourcesFor(f.File, f.URL, f.Size, f.Mirror)); err != nil {
		stop()
		return err
	}
	if spinner != nil {
		spinner.UpdateText("Extracting FFmpeg...")
	}
	targets := map[string]string{
		"bin/ffmpeg.exe":  filepath.Join(targetDir, "ffmpeg.exe"),
		"bin/ffprobe.exe": filepath.Join(targetDir, "ffprobe.exe"),
	}
	extracted, err := extractZipEntries(tmp.Name(), targets)
	stop()
	if err != nil {
		return err
	}
	if extracted < 2 {
		return fmt.Errorf("Downloader.go: downloadFFmpeg: only %d of 2 executables found in archive", extracted)
	}
	if err := os.WriteFile(filepath.Join(targetDir, ffmpegMarkerName), []byte(ffmpegMarker()), 0644); err != nil {
		return fmt.Errorf("Downloader.go: downloadFFmpeg (marker): %w", err)
	}
	pOK.Println("FFmpeg " + pins.FFmpeg.Version + " installed (verified).")
	return nil
}

// extractZipEntries extracts the entries whose path (after stripping the leading
// top-level directory) matches a key in targets, writing each to its mapped
// destination. Returns the number of files extracted.
func extractZipEntries(zipPath string, targets map[string]string) (int, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, fmt.Errorf("Downloader.go: extractZipEntries (open zip): %w", err)
	}
	defer zr.Close()

	extracted := 0
	for _, f := range zr.File {
		parts := strings.SplitN(filepath.ToSlash(f.Name), "/", 2)
		if len(parts) != 2 {
			continue
		}
		destPath, ok := targets[parts[1]]
		if !ok {
			continue
		}
		if err := writeZipFile(f, destPath); err != nil {
			return extracted, err
		}
		extracted++
	}
	return extracted, nil
}

func writeZipFile(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("Downloader.go: writeZipFile (open entry %q): %w", f.Name, err)
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("Downloader.go: writeZipFile (mkdir): %w", err)
	}
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("Downloader.go: writeZipFile (create %q): %w", filepath.Base(destPath), err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return fmt.Errorf("Downloader.go: writeZipFile (extract %q): %w", filepath.Base(destPath), err)
	}
	return out.Close()
}

// ----------------------------------------------------------------------------
// Python bootstrap: download embeddable Python, enable pip, install the engine,
// write the driver. Every step is idempotent; a half-finished install is
// completed on the next run because the readiness marker is written last.
// ----------------------------------------------------------------------------

// ensurePython guarantees Python\python.exe with the engine installed and
// Python\transcribe.py present. The marker stores what was installed — the
// pinned set's lockID, or a custom engine_spec — so a new SuperSubtitler with
// new pins (or a changed engine_spec) reinstalls the engine without
// re-downloading Python.
func ensurePython(ctx context.Context, pythonDir string) error {
	pyExe := filepath.Join(pythonDir, "python.exe")
	driver := filepath.Join(pythonDir, "transcribe.py")
	marker := filepath.Join(pythonDir, ".ss_ready")
	pinned := usesPinnedEngine(appSettings)
	want := appSettings.engineSpec
	if pinned {
		want = pins.lockID(appSettings.installCUDA)
	}

	if markerMatches(marker, want) && fileExists(pyExe) && fileExists(driver) {
		// A newer SuperSubtitler may ship a newer driver: keep it in sync even
		// when the engine itself does not need to be reinstalled.
		if cur, err := os.ReadFile(driver); err != nil || !bytes.Equal(cur, embeddedDriver) {
			if err := os.WriteFile(driver, embeddedDriver, 0644); err != nil {
				return fmt.Errorf("Downloader.go: ensurePython (update driver): %w", err)
			}
		}
		return nil
	}
	if !pinned {
		pWarn.Println("engine_spec or python_version differ from the defaults — installing the newest versions, " +
			"NOT the tested set (restore the defaults to get it back).")
	}

	if !fileExists(pyExe) {
		if err := installEmbeddedPython(ctx, pythonDir, pinned); err != nil {
			return err
		}
	}
	if err := enablePip(ctx, pythonDir, pyExe, pinned); err != nil {
		return err
	}
	if err := installEngine(ctx, pyExe, pinned); err != nil {
		return err
	}
	if err := os.WriteFile(driver, embeddedDriver, 0644); err != nil {
		return fmt.Errorf("Downloader.go: ensurePython (write driver): %w", err)
	}
	if err := os.WriteFile(marker, []byte(want), 0644); err != nil {
		return fmt.Errorf("Downloader.go: ensurePython (write marker): %w", err)
	}
	if pinned {
		pOK.Println("Python runtime ready (tested set: " + runtimeSummary() + ").")
	} else {
		pOK.Println("Python runtime ready (engine: " + appSettings.engineSpec + ").")
	}
	return nil
}

func markerMatches(marker, spec string) bool {
	data, err := os.ReadFile(marker)
	return err == nil && strings.TrimSpace(string(data)) == spec
}

func installEmbeddedPython(ctx context.Context, pythonDir string, pinned bool) error {
	label := "Downloading embedded Python " + appSettings.pythonVersion
	spinner, _ := pterm.DefaultSpinner.WithText(label + "...").Start()
	currentSpinner.Store(spinner)
	defer currentSpinner.Store(nil)
	var tmpPath string
	if pinned {
		tmp, err := os.CreateTemp("", "python-embed-*.zip")
		if err != nil {
			_ = spinner.Stop()
			return err
		}
		tmp.Close()
		tmpPath = tmp.Name()
		f := pins.Python
		if err := downloadPinned(ctx, "python", label, tmpPath, f.Size, f.SHA256, pins.sourcesFor(f.File, f.URL, f.Size, f.Mirror)); err != nil {
			os.Remove(tmpPath)
			_ = spinner.Stop()
			return err
		}
	} else {
		p, err := httpDownloadToFile(pythonEmbedURL(appSettings.pythonVersion), "python-embed-*.zip", "python", label, spinner)
		if err != nil {
			_ = spinner.Stop()
			return err
		}
		tmpPath = p
	}
	defer os.Remove(tmpPath)
	spinner.UpdateText("Extracting Python...")
	if err := extractZipAll(tmpPath, pythonDir); err != nil {
		_ = spinner.Stop()
		return err
	}
	if err := unlockEmbeddedSitePackages(pythonDir); err != nil {
		_ = spinner.Stop()
		return err
	}
	_ = spinner.Stop()
	pOK.Println("Embedded Python installed.")
	return nil
}

// unlockEmbeddedSitePackages rewrites pythonNNN._pth so the interpreter loads
// site-packages and runs site.main(); the embeddable disables both by default,
// which would otherwise hide pip and every installed package.
func unlockEmbeddedSitePackages(pythonDir string) error {
	pth := filepath.Join(pythonDir, pthName(appSettings.pythonVersion))
	parts := strings.Split(appSettings.pythonVersion, ".")
	zipName := "python3.zip"
	if len(parts) >= 2 {
		zipName = "python" + parts[0] + parts[1] + ".zip"
	}
	content := zipName + "\n.\nLib\\site-packages\nimport site\n"
	if err := os.WriteFile(pth, []byte(content), 0644); err != nil {
		return fmt.Errorf("Downloader.go: unlockEmbeddedSitePackages (write %s): %w", filepath.Base(pth), err)
	}
	return nil
}

// pipBootstrap installs the pip wheel given as argument with pip's own code
// loaded straight from that wheel.
const pipBootstrap = "import sys; sys.path.insert(0, sys.argv[1]); " +
	"from pip._internal.cli.main import main; " +
	"sys.exit(main(['install', '--no-index', '--no-warn-script-location', sys.argv[1]]))"

// enablePip installs pip once. Pinned: the pinned pip wheel (verified) runs
// itself to install itself (see pipBootstrap) — no get-pip.py, whose content
// changes over time.
// Custom: get-pip.py as before. Scripts\pip.exe is the install marker.
func enablePip(ctx context.Context, pythonDir, pyExe string, pinned bool) error {
	if fileExists(filepath.Join(pythonDir, "Scripts", "pip.exe")) {
		return nil
	}
	spinner, _ := pterm.DefaultSpinner.WithText("Bootstrapping pip...").Start()
	currentSpinner.Store(spinner)
	defer currentSpinner.Store(nil)
	stop := func() {
		if spinner != nil {
			_ = spinner.Stop()
		}
	}
	if pinned {
		f := pins.Pip
		whl := filepath.Join(os.TempDir(), f.File) // pip needs the real wheel name
		defer os.Remove(whl)
		if err := downloadPinned(ctx, "pip", "Downloading pip "+f.Version, whl, f.Size, f.SHA256, pins.sourcesFor(f.File, f.URL, f.Size, f.Mirror)); err != nil {
			stop()
			return err
		}
		emitSetup("pip", "progress", "Installing pip "+f.Version, 0, 0, 0)
		// pip runs from its own wheel to install itself. Started as "-c" and
		// not as "<wheel>/pip": pip on Windows refuses to modify itself when
		// its program name is "pip" (caught by the CI end-to-end test).
		if _, err := runProcess(ctx, pyExe, "-c", pipBootstrap, whl); err != nil {
			stop()
			return fmt.Errorf("Downloader.go: enablePip (pip wheel): %w", err)
		}
		stop()
		emitSetup("pip", "done", "pip "+f.Version+" installed", 0, 0, 0)
		return nil
	}
	getPip, err := httpDownloadToFile(getPipURL, "get-pip-*.py", "pip", "Downloading pip", spinner)
	if err != nil {
		stop()
		return err
	}
	defer os.Remove(getPip)
	emitSetup("pip", "progress", "Installing pip", 0, 0, 0)
	if _, err := runProcess(ctx, pyExe, getPip, "--no-warn-script-location"); err != nil {
		stop()
		return fmt.Errorf("Downloader.go: enablePip (get-pip): %w", err)
	}
	stop()
	emitSetup("pip", "done", "pip installed", 0, 0, 0)
	return nil
}

// installEngine installs the engine. Pinned: exactly the packages in
// runtime.json, each checked against its hash (--require-hashes also refuses
// any dependency that is not in the list). Custom: the engine_spec, newest.
func installEngine(ctx context.Context, pyExe string, pinned bool) error {
	spinner, _ := pterm.DefaultSpinner.WithText("Installing whisper engine (large, one-time)...").Start()
	defer func() { _ = spinner.Stop() }()
	if !pinned {
		spec := appSettings.engineSpec
		if err := runPip(ctx, pyExe, spinner, "engine", "Installing whisper engine", "--upgrade", spec); err != nil {
			return fmt.Errorf("Downloader.go: installEngine (pip install %s): %w", spec, err)
		}
		if appSettings.installCUDA {
			if err := runPip(ctx, pyExe, spinner, "cuda", "Installing CUDA libraries", cudaWheels...); err != nil {
				return fmt.Errorf("Downloader.go: installEngine (pip install cuda): %w", err)
			}
		}
		return nil
	}
	if err := installPinnedWheels(ctx, pyExe, spinner, "engine", "Installing whisper engine (tested set)", pins.packagesFor(false)); err != nil {
		return fmt.Errorf("Downloader.go: installEngine (pinned packages): %w", err)
	}
	if appSettings.installCUDA {
		if err := installPinnedWheels(ctx, pyExe, spinner, "cuda", "Installing CUDA libraries (tested set)", pins.packagesFor(true)); err != nil {
			return fmt.Errorf("Downloader.go: installEngine (pinned CUDA packages): %w", err)
		}
	}
	return nil
}

// installPinnedWheels installs exactly pkgs. Every wheel is fetched by
// SuperSubtitler itself — mirror first, PyPI second, each checked against its
// SHA-256 — into a temporary folder, and pip installs only from there
// (--no-index): it never asks PyPI on its own, and --require-hashes still
// refuses anything not in the list.
func installPinnedWheels(ctx context.Context, pyExe string, spinner *pterm.SpinnerPrinter, step, label string, pkgs []pinnedPackage) error {
	wheels, err := os.MkdirTemp("", "ss-wheels-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(wheels)
	var total int64
	for _, p := range pkgs {
		total += p.Size
	}
	currentSpinner.Store(spinner)
	defer currentSpinner.Store(nil)
	emitSetup(step, "start", fmt.Sprintf("%s — downloading %d packages (%.0f MB)", label, len(pkgs), float64(total)/bytesPerMB), 0, float64(total)/bytesPerMB, 0)
	for i, p := range pkgs {
		plabel := fmt.Sprintf("%s — package %d/%d: %s %s", label, i+1, len(pkgs), p.Name, p.Version)
		if err := downloadPinned(ctx, step, plabel, filepath.Join(wheels, p.File), p.Size, p.SHA256,
			pins.sourcesFor(p.File, p.URL, p.Size, pins.PackagesMirror)); err != nil {
			return err
		}
	}
	req, err := os.CreateTemp("", "ss-requirements-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(req.Name())
	if _, err := req.WriteString(requirementsFor(pkgs)); err != nil {
		req.Close()
		return err
	}
	req.Close()
	return runPip(ctx, pyExe, spinner, step, label, "--no-index", "--find-links", wheels,
		"--require-hashes", "--only-binary=:all:", "-r", req.Name())
}

// speakerPackageSpec is what an expert's own (unpinned) engine gets for
// speaker marking: the newest version, like the engine itself.
const speakerPackageSpec = "sherpa-onnx"

// ensureSpeakerEngine adds the speaker-marking package (sherpa-onnx, about
// 19 MB) to the installed Python the first time it is needed. It has its own
// marker, so the engine itself is never reinstalled for it.
func ensureSpeakerEngine(ctx context.Context, pythonDir string) error {
	pyExe := filepath.Join(pythonDir, "python.exe")
	marker := filepath.Join(pythonDir, ".ss_speakers")
	pinned := usesPinnedEngine(appSettings)
	want := speakerPackageSpec
	if pinned {
		want = pins.groupLockID("speakers")
	}
	if markerMatches(marker, want) {
		return nil
	}
	label := "Installing speaker recognition"
	spinner, _ := pterm.DefaultSpinner.WithText(label + " (one-time)...").Start()
	stop := func() {
		if spinner != nil {
			_ = spinner.Stop()
			spinner = nil
		}
	}
	defer stop()
	var err error
	if pinned {
		err = installPinnedWheels(ctx, pyExe, spinner, "speakers", label+" (tested set)", pins.groupPackages("speakers"))
	} else {
		err = runPip(ctx, pyExe, spinner, "speakers", label, "--upgrade", speakerPackageSpec)
	}
	if err != nil {
		return fmt.Errorf("Downloader.go: ensureSpeakerEngine: %w", err)
	}
	if err := os.WriteFile(marker, []byte(want), 0644); err != nil {
		return fmt.Errorf("Downloader.go: ensureSpeakerEngine (marker): %w", err)
	}
	stop()
	pOK.Println("Speaker recognition ready.")
	emitSetup("speakers", "done", "Speaker recognition ready", 0, 0, 0)
	return nil
}

// runPip runs "pip install" and shows what pip is doing (Collecting …,
// Downloading … (664.8 MB), Installing …) live, with the elapsed time, instead
// of one unchanging spinner line for many minutes. No stall watchdog: a single
// large CUDA wheel can legitimately print nothing for a long time on a slow
// line; the user can always stop with Ctrl+C.
func runPip(ctx context.Context, pyExe string, spinner *pterm.SpinnerPrinter, step, label string, pkgs ...string) error {
	started := time.Now()
	emitSetup(step, "start", label, 0, 0, 0)
	status := label
	var mu sync.Mutex
	show := func() {
		mu.Lock()
		text := status
		mu.Unlock()
		if spinner != nil {
			spinner.UpdateText(label + " — " + text + pterm.Gray("  · "+fmtDuration(time.Since(started))))
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	stopTick := make(chan struct{})
	defer close(stopTick)
	go func() {
		for {
			select {
			case <-tick.C:
				show()
			case <-stopTick:
				return
			}
		}
	}()
	onLine := func(line string) {
		if msg, ok := pipStatus(line); ok {
			mu.Lock()
			status = msg
			mu.Unlock()
			show()
			emitSetup(step, "progress", msg, 0, 0, 0)
		}
	}
	args := append([]string{"-m", "pip", "install", "--no-warn-script-location", "--progress-bar", "off"}, pkgs...)
	if err := runProcessStreamingStall(ctx, 0, onLine, pyExe, args...); err != nil {
		return err
	}
	emitSetup(step, "done", label, 0, 0, 0)
	return nil
}

// pipStatus picks the lines of pip's output worth showing as status.
func pipStatus(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, p := range []string{"Collecting ", "Downloading ", "Installing collected packages", "Successfully installed", "Requirement already satisfied"} {
		if strings.HasPrefix(line, p) {
			if len(line) > 110 {
				line = line[:107] + "..."
			}
			return line, true
		}
	}
	return "", false
}

// ----------------------------------------------------------------------------
// Speech models
// ----------------------------------------------------------------------------

// modelMarkerName lists the models already fetched ("name<TAB>path" per line).
const modelMarkerName = ".ss_models"

// modelPaths maps a model name to the folder it was installed in. Detection
// and transcription load that folder directly (see modelArg), so a pinned
// model is used exactly as verified, without asking any server again.
var modelPaths = map[string]string{}

// modelArg is what the engine is given for a model: its installed folder when
// known, else the name (the engine then resolves it itself).
func modelArg(name string) string {
	if p, ok := modelPaths[name]; ok && p != "" {
		return p
	}
	return name
}

// ensureModels fetches every listed speech model up front, so the (1.5–3 GB)
// download happens with visible progress here instead of silently inside the
// first language detection. Pinned models (runtime.json) come from the mirror
// first, at their exact revision, every file checked against its SHA-256.
// Other names (an expert's own choice in the INI) are fetched by the engine;
// those already recorded in the marker whose folder still exists are skipped
// without touching the network.
func ensureModels(ctx context.Context, models []string) error {
	marker := filepath.Join(modelDir, modelMarkerName)
	known := readModelMarker(marker)
	for _, m := range models {
		if pm, ok := pins.Models[m]; ok {
			path, err := ensurePinnedModel(ctx, m, pm)
			if err != nil {
				return err
			}
			modelPaths[m] = path
			continue
		}
		if p, ok := known[m]; ok && dirExists(p) {
			modelPaths[m] = p
			continue
		}
		pWarn.Println("Speech model " + m + " is not part of the tested set — using whatever its source currently offers.")
		path, err := fetchModel(ctx, m)
		if err != nil {
			return err
		}
		known[m] = path
		modelPaths[m] = path
		if err := writeModelMarker(marker, known); err != nil {
			pWarn.Println("Could not record the downloaded model: " + shortErr(err))
		}
	}
	return nil
}

// ensurePinnedModel makes sure whisper\models\pinned\<name>@<revision> holds
// every file of the pinned model. A file already there is kept when its hash
// matches (an interrupted download resumes with the next file).
func ensurePinnedModel(ctx context.Context, name string, pm pinnedModel) (string, error) {
	dir := filepath.Join(modelDir, "pinned", name+"@"+pm.Revision[:12])
	complete := filepath.Join(dir, ".complete")
	if markerMatches(complete, pm.Revision) {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("Downloader.go: ensurePinnedModel (mkdir): %w", err)
	}
	var total int64
	for _, f := range pm.Files {
		total += f.Size
	}
	size := fmt.Sprintf("%.1f GB", float64(total)/1e9)
	if total < 1e9 {
		size = fmt.Sprintf("%.0f MB", float64(total)/bytesPerMB)
	}
	label := fmt.Sprintf("Downloading model %s (%s, one-time)", name, size)
	spinner, _ := pterm.DefaultSpinner.WithText(label + "...").Start()
	currentSpinner.Store(spinner)
	defer currentSpinner.Store(nil)
	defer func() {
		if spinner != nil {
			_ = spinner.Stop()
		}
	}()
	emitSetup("model", "start", label, 0, float64(total)/bytesPerMB, 0)
	for _, f := range pm.Files {
		dest := filepath.Join(dir, f.Name)
		if info, err := os.Stat(dest); err == nil && info.Size() == f.Size {
			if got, err := sha256File(dest); err == nil && strings.EqualFold(got, f.SHA256) {
				continue
			}
		}
		if reuseCachedBlob(f, dest) {
			pInfo.Printf("Speech model %s: %s taken from the existing model cache (verified).\n", name, f.Name)
			continue
		}
		flabel := "Model " + name + ": " + f.Name
		sources := pins.sourcesFor(modelAsset(name, f.Name), hfURL(pm, f.Name), f.Size, pm.Mirror)
		if err := downloadPinned(ctx, "model", flabel, dest, f.Size, f.SHA256, sources); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(complete, []byte(pm.Revision), 0644); err != nil {
		return "", fmt.Errorf("Downloader.go: ensurePinnedModel (marker): %w", err)
	}
	if spinner != nil {
		_ = spinner.Stop()
		spinner = nil
	}
	pOK.Printf("Model %s ready (revision %s, verified).\n", name, pm.Revision[:12])
	emitSetup("model", "done", "Model "+name+" ready", float64(total)/bytesPerMB, float64(total)/bytesPerMB, 0)
	return dir, nil
}

// fetchModel runs the driver's --prefetch mode for one model while watching the
// model folder grow (the engine's own download bar is not visible from here).
func fetchModel(ctx context.Context, model string) (string, error) {
	label := "Downloading speech model " + model
	spinner, _ := pterm.DefaultSpinner.WithText(label + " (one-time, can take a while)...").Start()
	stop := func() {
		if spinner != nil {
			_ = spinner.Stop()
		}
	}
	emitSetup("model", "start", label, 0, 0, 0)
	before := dirSize(modelDir)
	started := time.Now()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				grown := dirSize(modelDir) - before
				if grown < 0 {
					grown = 0
				}
				rate := float64(grown) / bytesPerMB / time.Since(started).Seconds()
				if spinner != nil {
					spinner.UpdateText(progressText(label, grown, 0, rate))
				}
				emitSetup("model", "progress", label, float64(grown)/bytesPerMB, 0, rate)
			}
		}
	}()

	var path string
	onLine := func(line string) {
		if rest, ok := strings.CutPrefix(line, "@MODELPATH "); ok {
			if name, p, ok := strings.Cut(rest, "\t"); ok && name == model {
				path = strings.TrimSpace(p)
			}
		}
	}
	err := runProcessStreamingStall(ctx, 0, onLine, pythonExePath, whisperDriverPath, "--prefetch", fModelDir, modelDir, model)
	close(done)
	stop()
	if err != nil {
		return "", fmt.Errorf("Downloader.go: fetchModel (%s): %w", model, err)
	}
	if path == "" {
		return "", fmt.Errorf("Downloader.go: fetchModel (%s): engine reported no model folder", model)
	}
	grown := dirSize(modelDir) - before
	if grown > bytesPerMB {
		pOK.Printf("Speech model %s ready (%.0f MB downloaded).\n", model, float64(grown)/bytesPerMB)
	}
	emitSetup("model", "done", "Speech model "+model+" ready", float64(max(grown, 0))/bytesPerMB, 0, 0)
	return path, nil
}

// reuseCachedBlob looks for the file in the engine's own model cache, where an
// older SuperSubtitler stored the same models: Hugging Face names large files
// there by their SHA-256 (models--<org>--<name>\blobs\<sha256>). A match is
// linked (or copied) instead of downloading gigabytes again — after checking
// its hash like any download.
func reuseCachedBlob(f modelFile, dest string) bool {
	matches, _ := filepath.Glob(filepath.Join(modelDir, "models--*", "blobs", strings.ToLower(f.SHA256)))
	for _, src := range matches {
		info, err := os.Stat(src)
		if err != nil || info.Size() != f.Size {
			continue
		}
		if got, err := sha256File(src); err != nil || !strings.EqualFold(got, f.SHA256) {
			continue
		}
		_ = os.Remove(dest)
		if os.Link(src, dest) == nil {
			return true
		}
		if copyFile(src, dest) == nil {
			return true
		}
		_ = os.Remove(dest)
	}
	return false
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest + ".copy")
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest + ".copy")
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dest + ".copy")
		return err
	}
	return os.Rename(dest+".copy", dest)
}

func readModelMarker(path string) map[string]string {
	known := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return known
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, p, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t"); ok && name != "" {
			known[name] = p
		}
	}
	return known
}

func writeModelMarker(path string, known map[string]string) error {
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + "\t" + known[n] + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// httpDownloadToFile streams url into a temp file named via pattern and returns
// its path; the caller removes it. Errors clean up the partial file. Progress is
// shown on spinner (may be nil) and reported as "setup" events for step.
func httpDownloadToFile(url, pattern, step, label string, spinner *pterm.SpinnerPrinter) (string, error) {
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("Downloader.go: httpDownloadToFile (create temp): %w", err)
	}
	tmpPath := tmp.Name()
	resp, err := httpClient.Get(url)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Downloader.go: httpDownloadToFile (http.Get): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Downloader.go: httpDownloadToFile: server returned HTTP %d for %s", resp.StatusCode, url)
	}
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	prog := newDLProgress(step, label, total, spinner)
	if _, err := io.Copy(io.MultiWriter(tmp, prog), resp.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Downloader.go: httpDownloadToFile (stream to disk): %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Downloader.go: httpDownloadToFile (close temp): %w", err)
	}
	prog.finish()
	return tmpPath, nil
}

// extractZipAll extracts every entry into targetDir, stripping a single leading
// top-level directory if the whole archive shares one.
func extractZipAll(zipPath, targetDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("Downloader.go: extractZipAll (open zip): %w", err)
	}
	defer zr.Close()

	root := commonTopDir(zr.File)
	for _, f := range zr.File {
		rel := filepath.ToSlash(f.Name)
		if root != "" {
			rel = strings.TrimPrefix(rel, root+"/")
		}
		if rel == "" {
			continue
		}
		dest := filepath.Join(targetDir, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0755); err != nil {
				return fmt.Errorf("Downloader.go: extractZipAll (mkdir): %w", err)
			}
			continue
		}
		if err := writeZipFile(f, dest); err != nil {
			return err
		}
	}
	return nil
}

func commonTopDir(files []*zip.File) string {
	root := ""
	for _, f := range files {
		parts := strings.SplitN(filepath.ToSlash(f.Name), "/", 2)
		if len(parts) < 2 {
			return ""
		}
		if root == "" {
			root = parts[0]
		} else if root != parts[0] {
			return ""
		}
	}
	return root
}
