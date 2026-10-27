//go:build windows && amd64

package main

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
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

// BtbN removed the floating "latest" release tag (the old fixed download URL now
// returns 404), so the newest autobuild is resolved at runtime via the GitHub API.
const ffmpegReleasesAPI = "https://api.github.com/repos/BtbN/FFmpeg-Builds/releases?per_page=5"

type ghRelease struct {
	Assets []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

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

// resolveFFmpegDownloadURL returns the download URL of the newest static win64
// GPL master build (the flavour the old "latest" tag pointed at). Fallback: any
// non-shared win64 GPL zip from the newest releases.
func resolveFFmpegDownloadURL() (string, error) {
	req, err := http.NewRequest(http.MethodGet, ffmpegReleasesAPI, nil)
	if err != nil {
		return "", fmt.Errorf("Downloader.go: resolveFFmpegDownloadURL: %w", err)
	}
	req.Header.Set("User-Agent", "SuperSubtitler") // GitHub rejects requests without one
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Downloader.go: resolveFFmpegDownloadURL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Downloader.go: resolveFFmpegDownloadURL: GitHub API returned HTTP %d", resp.StatusCode)
	}

	var releases []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&releases); err != nil {
		return "", fmt.Errorf("Downloader.go: resolveFFmpegDownloadURL (decode): %w", err)
	}
	var fallback string
	for _, rel := range releases {
		for _, a := range rel.Assets {
			n := strings.ToLower(a.Name)
			if !strings.HasSuffix(n, ".zip") || !strings.Contains(n, "win64-gpl") || strings.Contains(n, "shared") {
				continue
			}
			// Master build, e.g. "ffmpeg-N-124941-g54749da98a-win64-gpl.zip".
			if strings.HasPrefix(n, "ffmpeg-n-") && strings.HasSuffix(n, "-win64-gpl.zip") {
				return a.BrowserDownloadURL, nil
			}
			if fallback == "" {
				fallback = a.BrowserDownloadURL
			}
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("Downloader.go: resolveFFmpegDownloadURL: no win64-gpl zip found in the latest releases")
}

// embeddedDriver is transcribe.py, written into Python\ on first run and kept
// in sync with the installed engine version.
//
//go:embed transcribe.py
var embeddedDriver []byte

const getPipURL = "https://bootstrap.pypa.io/get-pip.py"

// cudaWheels are the pip packages that ship the CUDA cuBLAS/cuDNN DLLs, so the
// GPU path works without a system-wide CUDA toolkit. cu12 matches CTranslate2's
// CUDA 12 builds used by current faster-whisper. If a future engine moves to a
// new CUDA generation, change these two names.
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

// downloadFFmpeg streams the official BtbN GPL build to a temp file and extracts
// only ffmpeg.exe and ffprobe.exe into targetDir.
func downloadFFmpeg(targetDir string) error {
	spinner, _ := pterm.DefaultSpinner.WithText("Looking up the latest FFmpeg build...").Start()
	stop := func() {
		if spinner != nil {
			_ = spinner.Stop()
		}
	}
	emitSetup("ffmpeg", "start", "Looking up the latest FFmpeg build", 0, 0, 0)

	downloadURL, err := resolveFFmpegDownloadURL()
	if err != nil {
		stop()
		return err
	}
	tmpPath, err := httpDownloadToFile(downloadURL, "ffmpeg-download-*.zip", "ffmpeg", "Downloading FFmpeg", spinner)
	if err != nil {
		stop()
		return err
	}
	defer os.Remove(tmpPath)
	if spinner != nil {
		spinner.UpdateText("Extracting FFmpeg...")
	}

	targets := map[string]string{
		"bin/ffmpeg.exe":  filepath.Join(targetDir, "ffmpeg.exe"),
		"bin/ffprobe.exe": filepath.Join(targetDir, "ffprobe.exe"),
	}
	extracted, err := extractZipEntries(tmpPath, targets)
	stop()
	if err != nil {
		return err
	}
	if extracted < 2 {
		return fmt.Errorf("Downloader.go: downloadFFmpeg: only %d of 2 executables found in archive", extracted)
	}
	pOK.Println("FFmpeg auto-download complete: ffmpeg.exe + ffprobe.exe installed.")
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
// Python\transcribe.py present. The marker file stores the engine spec, so a
// changed engine_spec re-installs the engine without re-downloading Python.
func ensurePython(ctx context.Context, pythonDir string) error {
	pyExe := filepath.Join(pythonDir, "python.exe")
	driver := filepath.Join(pythonDir, "transcribe.py")
	marker := filepath.Join(pythonDir, ".ss_ready")
	spec := appSettings.engineSpec

	if markerMatches(marker, spec) && fileExists(pyExe) && fileExists(driver) {
		// A newer SuperSubtitler may ship a newer driver: keep it in sync even
		// when the engine itself does not need to be reinstalled.
		if cur, err := os.ReadFile(driver); err != nil || !bytes.Equal(cur, embeddedDriver) {
			if err := os.WriteFile(driver, embeddedDriver, 0644); err != nil {
				return fmt.Errorf("Downloader.go: ensurePython (update driver): %w", err)
			}
		}
		return nil
	}

	if !fileExists(pyExe) {
		if err := installEmbeddedPython(pythonDir); err != nil {
			return err
		}
	}
	if err := enablePip(ctx, pythonDir, pyExe); err != nil {
		return err
	}
	if err := installEngine(ctx, pyExe, spec); err != nil {
		return err
	}
	if err := os.WriteFile(driver, embeddedDriver, 0644); err != nil {
		return fmt.Errorf("Downloader.go: ensurePython (write driver): %w", err)
	}
	if err := os.WriteFile(marker, []byte(spec), 0644); err != nil {
		return fmt.Errorf("Downloader.go: ensurePython (write marker): %w", err)
	}
	pOK.Println("Python runtime ready (engine: " + spec + ").")
	return nil
}

func markerMatches(marker, spec string) bool {
	data, err := os.ReadFile(marker)
	return err == nil && strings.TrimSpace(string(data)) == spec
}

func installEmbeddedPython(pythonDir string) error {
	label := "Downloading embedded Python " + appSettings.pythonVersion
	spinner, _ := pterm.DefaultSpinner.WithText(label + "...").Start()
	tmpPath, err := httpDownloadToFile(pythonEmbedURL(appSettings.pythonVersion), "python-embed-*.zip", "python", label, spinner)
	if err != nil {
		_ = spinner.Stop()
		return err
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

// enablePip downloads and runs get-pip.py once. pip is invoked later via
// "python.exe -m pip", so the presence of Scripts\pip.exe is the install marker.
func enablePip(ctx context.Context, pythonDir, pyExe string) error {
	if fileExists(filepath.Join(pythonDir, "Scripts", "pip.exe")) {
		return nil
	}
	spinner, _ := pterm.DefaultSpinner.WithText("Bootstrapping pip...").Start()
	getPip, err := httpDownloadToFile(getPipURL, "get-pip-*.py", "pip", "Downloading pip", spinner)
	if err != nil {
		_ = spinner.Stop()
		return err
	}
	defer os.Remove(getPip)
	emitSetup("pip", "progress", "Installing pip", 0, 0, 0)
	if _, err := runProcess(ctx, pyExe, getPip, "--no-warn-script-location"); err != nil {
		_ = spinner.Stop()
		return fmt.Errorf("Downloader.go: enablePip (get-pip): %w", err)
	}
	_ = spinner.Stop()
	emitSetup("pip", "done", "pip installed", 0, 0, 0)
	return nil
}

// installEngine pip-installs the configured engine spec and, when enabled, the
// CUDA wheels. --upgrade keeps the engine current on every (re-)install.
func installEngine(ctx context.Context, pyExe, spec string) error {
	spinner, _ := pterm.DefaultSpinner.WithText("Installing whisper engine (large, one-time)...").Start()
	if err := runPip(ctx, pyExe, spinner, "engine", "Installing whisper engine", "--upgrade", spec); err != nil {
		_ = spinner.Stop()
		return fmt.Errorf("Downloader.go: installEngine (pip install %s): %w", spec, err)
	}
	if appSettings.installCUDA {
		if err := runPip(ctx, pyExe, spinner, "cuda", "Installing CUDA libraries", cudaWheels...); err != nil {
			_ = spinner.Stop()
			return fmt.Errorf("Downloader.go: installEngine (pip install cuda): %w", err)
		}
	}
	_ = spinner.Stop()
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

// ensureModels fetches every listed speech model up front, so the (1.5–3 GB)
// download happens with visible progress here instead of silently inside the
// first language detection. Models already recorded in the marker whose folder
// still exists are skipped without touching the network.
func ensureModels(ctx context.Context, models []string) error {
	marker := filepath.Join(modelDir, modelMarkerName)
	known := readModelMarker(marker)
	for _, m := range models {
		if p, ok := known[m]; ok && dirExists(p) {
			continue
		}
		path, err := fetchModel(ctx, m)
		if err != nil {
			return err
		}
		known[m] = path
		if err := writeModelMarker(marker, known); err != nil {
			pWarn.Println("Could not record the downloaded model: " + shortErr(err))
		}
	}
	return nil
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
