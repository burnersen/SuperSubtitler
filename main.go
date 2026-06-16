//go:build windows && amd64

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pterm/pterm"
)

// appVersion is shown in the startup header so the running build is obvious.
// Keep it in sync with the GitHub release tag on every release.
const appVersion = "0.1.0"

// Embedded source archive, self-extracted into src/ on first run only. The
// build step regenerates SuperSubtitler_src.zip from the .go sources.
//
//go:embed SuperSubtitler_src.zip
var embeddedSource []byte

// pterm prefix printers, shared across the package.
var (
	pInfo  = &pterm.Info
	pWarn  = &pterm.Warning
	pErr   = &pterm.Error
	pOK    = &pterm.Success
	pAbort = pterm.Error.WithPrefix(pterm.Prefix{Text: "ABORT", Style: pterm.NewStyle(pterm.FgLightRed)})
)

// Tool paths, resolved once in initTools.
var (
	ffmpegPath  string
	ffprobePath string
)

var videoExtensions = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".m4v": true,
	".ts": true, ".mts": true, ".m2ts": true, ".wmv": true, ".flv": true,
	".webm": true, ".mpg": true, ".mpeg": true, ".vob": true, ".3gp": true,
}

// audioExtensions are accepted as audio-only inputs. They are transcribed like
// video but produce only a .srt (no MKV, since there is no video to mux into).
var audioExtensions = map[string]bool{
	".m4a": true, ".mp3": true, ".wav": true, ".flac": true,
	".aac": true, ".ogg": true, ".opus": true, ".m4b": true,
}

func isAudioFile(path string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(path))]
}

func main() {
	defer crashCatcher()

	enableAnsiConsole()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setupSignalContext(cancel)
	setupConsoleCtrlHandler(cancel)

	printHeader()

	if err := initJobObject(); err != nil {
		pWarn.Println("Job object unavailable — orphan-kill guarantee weakened: " + err.Error())
	}

	loadOrCreateAppConfig()
	activeCleaner = loadCleanerConfig()
	_ = writeHelpFileIfMissing()

	cfg := parseArgs(os.Args[1:])
	lowMemActive = cfg.lowMem

	if cfg.help || len(cfg.inputArgs) == 0 {
		printUsage()
		waitForEnter()
		return
	}

	printQuickTips()

	if err := initTools(ctx); err != nil {
		pErr.Println(err.Error())
		waitForEnter()
		return
	}

	selfExtractSource()

	// Auto-detect a merge job (1 video + dropped audio/subtitle files) and offer
	// to mux them into one file, before falling through to normal subtitling.
	if !cfg.split {
		if video, audios, subs, ok := classifyMergeInputs(cfg.inputArgs); ok {
			if askMerge(video, audios, subs) {
				runMergeMode(ctx, video, audios, subs)
				waitForEnter()
				return
			}
		}
	}

	files, skipped := collectInputFiles(cfg.inputArgs)
	if skipped > 0 {
		pInfo.Printf("Skipped %d file(s) that already have subtitles or are tool output.\n", skipped)
	}
	if len(files) == 0 {
		pWarn.Println("No processable video/audio files. Drag files or folders onto the EXE.")
		waitForEnter()
		return
	}
	pInfo.Printf("Queued %d file(s).\n", len(files))

	if cfg.split {
		runDiagnosticMode(ctx, cfg, files)
		waitForEnter()
		return
	}

	jobs := buildFileJobs(ctx, cfg, files)
	results := runPipeline(ctx, cfg, jobs)
	printSummary(results, ctx.Err() != nil)
	waitForEnter()
}

// ----------------------------------------------------------------------------
// Init
// ----------------------------------------------------------------------------

func crashCatcher() {
	if r := recover(); r != nil {
		pErr.Printf("Unexpected error: %v\n", r)
		fmt.Println(string(debug.Stack()))
		waitForEnter()
	}
}

func setupSignalContext(cancel func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		pWarn.Println("Interrupt received — finishing current step, then stopping...")
		cancel()
		// A second interrupt means the user wants out now, not after the current
		// ffmpeg/whisper step finishes — hard-exit instead of waiting.
		<-ch
		pErr.Println("Second interrupt — exiting now.")
		os.Exit(1)
	}()
}

func printHeader() {
	content := pterm.NewStyle(pterm.FgLightCyan, pterm.Bold).Sprint("SuperSubtitler v"+appVersion) + "\n" +
		pterm.Gray("automatic subtitles · faster-whisper · self-setup Python")
	pterm.DefaultBox.Println(content)
	pterm.Println()
}

// printQuickTips shows a compact box of the most useful flags and some language
// codes on every working start, mirroring the sibling converter's quick-start box.
func printQuickTips() {
	y := pterm.LightYellow
	g := pterm.Gray
	tips := y("• drag video/audio files or a folder onto the EXE") + "\n" +
		y("• drop 1 video + audio/subtitle files") + g("   >>  ") + "merge into one .joined.mkv\n" +
		y("• -translate <files>") + g("        >>  ") + "translate everything to English\n" +
		y("• -lang:de <files>  ") + g("        >>  ") + "force the source language (skip detection)\n" +
		y("• -track:N  /  -channel:N") + g("   >>  ") + "pick a specific audio track / channel\n" +
		y("• -split <files>    ") + g("        >>  ") + "export each audio channel as WAV (find the speech)\n" +
		y("• -srtonly · -lean · -lowmem · -novad") + g("   >>  ") + "see -help for all options\n\n" +
		g("  language codes: de en es fr it pt nl ru pl tr ja ko zh ar …  (any ISO code)")
	pterm.DefaultBox.WithTitle(pterm.LightCyan("  Quick start  ")).WithTitleTopCenter().Println(tips)
	pterm.Println()
}

// printUsage lists every user-facing flag and the accepted formats. Shown when
// no input is given or on -help, so flags are discoverable without a manual.
func printUsage() {
	head := func(s string) { pterm.Println(pterm.NewStyle(pterm.FgYellow, pterm.Bold).Sprint(s)) }

	head("Usage")
	pterm.Println(pterm.Gray("  Drag video/audio files or folders onto the EXE, or pass them as arguments."))
	pterm.Println(pterm.Gray("  Flags and files may be given in any order."))
	pterm.Println()

	head("Options")
	rows := pterm.TableData{
		{pterm.LightCyan("-lang:XX"), "Force source language (ISO code, e.g. -lang:de). Default: auto-detect."},
		{pterm.LightCyan("-translate"), "Translate every track to English (uses the large model)."},
		{pterm.LightCyan("-track:N"), "Use audio track N (1-based) instead of auto-selecting."},
		{pterm.LightCyan("-channel:N"), "Use physical channel N (0-based) of the chosen track."},
		{pterm.LightCyan("-srtonly"), "Write only the .srt sidecar, no MKV."},
		{pterm.LightCyan("-lean"), "MKV keeps only video + chosen audio + the subtitle track."},
		{pterm.LightCyan("-novad"), "Disable the voice-activity (silence) filter."},
		{pterm.LightCyan("-lowmem"), "Smaller compute type (less VRAM, slightly slower)."},
		{pterm.LightCyan("-split"), "Export each audio channel as a WAV (to find the speech); skip transcription."},
		{pterm.LightCyan("-help"), "Show this help."},
	}
	_ = pterm.DefaultTable.WithData(rows).Render()
	pterm.Println()

	head("Supported formats")
	pterm.Println(pterm.Gray("  Video → .subbed.mkv:  mkv, mp4, mov, m4v, avi, ts, m2ts, webm, mpg, wmv, flv, 3gp"))
	pterm.Println(pterm.Gray("  Audio → .srt only:    m4a, mp3, wav, flac, aac, ogg, opus, m4b"))
}

// parseProgress extracts the fraction from an "@PROGRESS 0.42" line emitted by
// transcribe.py. ok is false for any other stdout line.
func parseProgress(line string) (float64, bool) {
	const pfx = "@PROGRESS "
	if !strings.HasPrefix(line, pfx) {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(line[len(pfx):]), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// etaSuffix renders a " · ETA mm:ss" hint from elapsed wall time and progress.
// Whisper runs roughly linearly with audio position, so this stays trustworthy.
func etaSuffix(start time.Time, frac float64) string {
	if frac <= 0.02 {
		return ""
	}
	elapsed := time.Since(start).Seconds()
	remain := elapsed * (1 - frac) / frac
	if remain < 0 {
		remain = 0
	}
	return pterm.Gray("  · ETA " + fmtDuration(time.Duration(remain)*time.Second))
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func initTools(ctx context.Context) error {
	dir := exeDir()
	ffmpegPath = filepath.Join(dir, "ffmpeg.exe")
	ffprobePath = filepath.Join(dir, "ffprobe.exe")

	if !fileExists(ffmpegPath) || !fileExists(ffprobePath) {
		pInfo.Println("FFmpeg not found next to the EXE — downloading once...")
		if err := downloadFFmpeg(dir); err != nil {
			return err
		}
	}

	whisperDir := filepath.Join(dir, "whisper")
	modelDir = filepath.Join(whisperDir, "models")
	if err := os.MkdirAll(modelDir, 0755); err != nil {
		return fmt.Errorf("main.go: initTools (mkdir models): %w", err)
	}

	pythonDir := filepath.Join(dir, "Python")
	pythonExePath = filepath.Join(pythonDir, "python.exe")
	whisperDriverPath = filepath.Join(pythonDir, "transcribe.py")
	if err := ensurePython(ctx, pythonDir); err != nil {
		return err
	}
	return nil
}

// ----------------------------------------------------------------------------
// Self-extract embedded source (first run only, never overwrite)
// ----------------------------------------------------------------------------

func selfExtractSource() {
	srcDir := filepath.Join(exeDir(), "src")
	if _, err := os.Stat(srcDir); err == nil {
		return // already present — never overwrite
	}
	if len(embeddedSource) == 0 {
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(embeddedSource), int64(len(embeddedSource)))
	if err != nil {
		pWarn.Println("Could not read embedded source: " + err.Error())
		return
	}
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return
	}
	for _, f := range zr.File {
		dest := filepath.Join(srcDir, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dest, 0755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(dest), 0755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			continue
		}
		_, _ = io.Copy(out, rc)
		out.Close()
		rc.Close()
	}
	pInfo.Println("Extracted bundled source into " + pterm.LightCyan("src\\") + " (first run).")
}

// ----------------------------------------------------------------------------
// Argument parsing
// ----------------------------------------------------------------------------

func parseArgs(argv []string) *AppConfig {
	cfg := &AppConfig{forcedChannel: -1}
	for _, a := range argv {
		low := strings.ToLower(a)
		switch {
		case strings.HasPrefix(low, "-track:"):
			cfg.forcedTrack = atoiDefault(a[len("-track:"):], 0)
		case strings.HasPrefix(low, "-channel:"):
			cfg.forcedChannel = atoiDefault(a[len("-channel:"):], -1)
		case strings.HasPrefix(low, "-lang:"):
			cfg.forcedLang = a[len("-lang:"):]
		case low == "-translate" || low == "-en":
			cfg.translate = true
		case low == "-split" || low == "-diag":
			cfg.split = true
		case low == "-srtonly":
			cfg.srtOnly = true
		case low == "-lean":
			cfg.lean = true
		case low == "-novad":
			cfg.noVad = true
		case low == "-lowmem":
			cfg.lowMem = true
		case low == "-help" || low == "-h" || low == "-?" || low == "--help":
			cfg.help = true
		case strings.HasPrefix(a, "-"):
			pWarn.Println("Ignoring unknown flag: " + a)
		default:
			cfg.inputArgs = append(cfg.inputArgs, a)
		}
	}
	return cfg
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// ----------------------------------------------------------------------------
// File collection + skip rules
// ----------------------------------------------------------------------------

func collectInputFiles(args []string) ([]string, int) {
	seen := map[string]bool{}
	var files []string
	skipped := 0

	consider := func(path string) {
		ext := strings.ToLower(filepath.Ext(path))
		if !videoExtensions[ext] && !audioExtensions[ext] {
			return
		}
		if strings.HasSuffix(strings.ToLower(path), ".subbed.mkv") {
			return // our own output, never re-feed
		}
		if hasExistingSubs(path) {
			skipped++
			return
		}
		long := getLongPathName(path)
		key := strings.ToLower(long)
		if seen[key] {
			return
		}
		seen[key] = true
		files = append(files, long)
	}

	for _, in := range args {
		info, err := os.Stat(in)
		if err != nil {
			pWarn.Println("Cannot access: " + in)
			continue
		}
		if info.IsDir() {
			filepath.WalkDir(in, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				consider(path)
				return nil
			})
		} else {
			consider(in)
		}
	}
	return files, skipped
}

// hasExistingSubs reports whether a sidecar .srt or a .subbed.mkv already exists
// for this video, so a re-scan skips finished work.
func hasExistingSubs(path string) bool {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	if fileExists(base + ".subbed.mkv") {
		return true
	}
	if m, _ := filepath.Glob(base + ".*.srt"); len(m) > 0 {
		return true
	}
	return false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// ----------------------------------------------------------------------------
// Pipeline (8b): extract N+1 overlaps transcribe N; mux N overlaps transcribe
// N+1. The GPU/whisper stage is strictly serial (single goroutine).
// ----------------------------------------------------------------------------

type fileJob struct {
	index    int
	src      string
	work     string
	duration float64

	tracks []*trackJob // one per selected audio stream

	status  string // "ok" | "error" | "skipped"
	outPath string
	err     error
}

// trackJob is one selected audio stream of a file: extracted, transcribed and
// turned into its own language-tagged subtitle. A single track may fail without
// failing the whole file, as long as at least one track survives.
type trackJob struct {
	stream    probeStream
	ordinal   int // 0-based index among the file's audio streams (for -map 0:a:N)
	detChunks []string
	mainWav   string
	plan      langPlan
	wj        *whisperJSON
	srtPath   string
	outLang   string
	ok        bool
	err       error
}

func (j *fileJob) label() string {
	return fmt.Sprintf("[%d] %s", j.index+1, baseName(j.src))
}

// recoverJob turns a panic inside one pipeline stage into a failed job instead
// of a process crash, so one bad file can never take the whole batch (or the
// final summary + "press Enter") down with it.
func recoverJob(stage string, job *fileJob) {
	if r := recover(); r != nil {
		pErr.Printf("Internal error in %s (%s): %v\n", stage, job.label(), r)
		if job.err == nil {
			job.fail(fmt.Errorf("panic in %s: %v", stage, r))
		}
	}
}

// buildFileJobs probes each input once and resolves which audio tracks to
// subtitle (interactive menu when a file has several and no -track is forced).
// Done up front, before the pipeline starts, so all questions are asked before
// the unattended processing begins. A probe/selection failure yields an
// already-failed job so it still shows up in the summary.
func buildFileJobs(ctx context.Context, cfg *AppConfig, files []string) []*fileJob {
	var jobs []*fileJob
	for i, f := range files {
		if ctx.Err() != nil {
			break
		}
		job := &fileJob{index: i, src: f}
		pd, err := probeFile(ctx, f)
		if err != nil {
			job.fail(err)
			jobs = append(jobs, job)
			continue
		}
		job.duration = pd.durationSec()
		if len(pd.audioStreams()) > 1 && cfg.forcedTrack <= 0 {
			pInfo.Printf("%s\n", job.label()) // context for the upcoming question
		}
		sels, err := selectTracks(cfg, pd)
		if err != nil {
			job.fail(err)
			jobs = append(jobs, job)
			continue
		}
		for _, s := range sels {
			job.tracks = append(job.tracks, &trackJob{stream: s.stream, ordinal: s.ordinal})
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// firstTrackErr returns the first per-track error, or a fallback if none was set.
func firstTrackErr(job *fileJob, fallback string) error {
	for _, t := range job.tracks {
		if t.err != nil {
			return t.err
		}
	}
	return errors.New(fallback)
}

func runPipeline(ctx context.Context, cfg *AppConfig, jobs []*fileJob) []*fileJob {
	extractCh := make(chan *fileJob, 2)
	muxCh := make(chan *fileJob, 2)
	var results []*fileJob
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Stage 1: extraction (prefetch).
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(extractCh)
		for _, job := range jobs {
			if ctx.Err() != nil {
				return
			}
			func() {
				defer recoverJob("extract", job)
				extractStage(ctx, cfg, job)
			}()
			extractCh <- job
		}
	}()

	// Stage 3: finalize (SRT + mux), trailing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for job := range muxCh {
			func() {
				defer recoverJob("finalize", job)
				finalizeStage(ctx, cfg, job)
			}()
			mu.Lock()
			results = append(results, job)
			mu.Unlock()
		}
	}()

	// Stage 2: GPU (serial) — runs on this goroutine.
	for job := range extractCh {
		func() {
			defer recoverJob("transcribe", job)
			gpuStage(ctx, cfg, job)
		}()
		muxCh <- job
	}
	close(muxCh)
	wg.Wait()
	return results
}

func extractStage(ctx context.Context, cfg *AppConfig, job *fileJob) {
	if job.err != nil {
		return // already failed during probing/selection
	}
	if ctx.Err() != nil {
		job.fail(ctx.Err())
		return
	}
	work, err := os.MkdirTemp("", "subs-*")
	if err != nil {
		job.fail(fmt.Errorf("create work dir: %w", err))
		return
	}
	job.work = work

	alive := 0
	for i, t := range job.tracks {
		if ctx.Err() != nil {
			job.fail(ctx.Err())
			return
		}
		tdir := filepath.Join(work, fmt.Sprintf("t%d", i))
		chunks, err := extractDetectionChunks(ctx, cfg, job.src, t.stream, t.ordinal, job.duration, filepath.Join(tdir, "det"))
		if err != nil {
			t.err = err
			continue
		}
		t.detChunks = chunks
		mainWav := filepath.Join(tdir, "main.wav")
		if err := extractMainWAV(ctx, cfg, job.src, t.stream, t.ordinal, mainWav); err != nil {
			t.err = err
			continue
		}
		t.mainWav = mainWav
		alive++
	}
	if alive == 0 {
		job.fail(firstTrackErr(job, "audio extraction failed for all tracks"))
	}
}

func gpuStage(ctx context.Context, cfg *AppConfig, job *fileJob) {
	if job.err != nil || ctx.Err() != nil {
		if job.err == nil {
			job.fail(ctx.Err())
		}
		return
	}
	multi := len(job.tracks) > 1
	for i, t := range job.tracks {
		if t.err != nil || t.mainWav == "" {
			continue // extraction already failed for this track
		}
		if ctx.Err() != nil {
			job.fail(ctx.Err())
			return
		}
		tdir := filepath.Join(job.work, fmt.Sprintf("t%d", i))
		tlabel := job.label()
		if multi {
			tlabel = fmt.Sprintf("%s (track %d/%d)", job.label(), i+1, len(job.tracks))
		}

		pInfo.Printf("%s — detecting language...\n", tlabel)
		detected, err := detectLanguage(ctx, t.detChunks, filepath.Join(tdir, "detout"))
		if err != nil {
			t.err = err
			continue
		}
		src := decideLanguage(cfg, containerLang(t.stream), detected)
		t.plan = buildPlan(cfg, src)
		t.outLang = t.plan.outLang

		pInfo.Printf("%s — %s with %s (out: %s)...\n",
			tlabel, t.plan.task, t.plan.model, t.plan.outLang)

		bar, _ := pterm.DefaultProgressbar.
			WithTotal(1000).
			WithShowCount(false).
			WithShowPercentage(true).
			WithShowElapsedTime(true).
			WithMaxWidth(80).
			WithTitle(tlabel + " transcribing").
			Start()
		started := time.Now()
		onLine := func(line string) {
			f, ok := parseProgress(line)
			if !ok || bar == nil {
				return
			}
			target := int(f * 1000)
			if target < 0 {
				target = 0
			} else if target > 1000 {
				target = 1000
			}
			bar.Current = target
			bar.UpdateTitle(tlabel + " transcribing" + etaSuffix(started, f))
		}

		wj, err := transcribe(ctx, cfg, t.mainWav, t.plan.model, t.plan.task, t.plan.srcLang, filepath.Join(tdir, "txout"), onLine)
		if bar != nil {
			bar.Current = 1000
			_, _ = bar.Stop()
		}
		if err != nil {
			t.err = err
			continue
		}
		t.wj = wj
	}
}

func finalizeStage(ctx context.Context, cfg *AppConfig, job *fileJob) {
	defer func() {
		if job.work != "" {
			_ = os.RemoveAll(job.work)
		}
	}()
	if job.err != nil {
		return
	}
	leadinSec := float64(appSettings.leadinMs) / 1000.0
	base := strings.TrimSuffix(job.src, filepath.Ext(job.src))
	usedSRT := map[string]bool{}

	var subs []subInput
	var audioOrdinals []int
	for _, t := range job.tracks {
		if t.wj == nil {
			continue // track failed earlier
		}
		srt := buildSRT(t.wj, leadinSec, appSettings)
		srt = cleanSRT(srt, activeCleaner)
		if strings.TrimSpace(srt) == "" {
			t.err = errors.New("no speech recognized")
			continue
		}
		srtPath := uniqueSRTPath(base, t.outLang, usedSRT)
		if err := writeSRT(srtPath, srt); err != nil {
			t.err = err
			continue
		}
		t.srtPath = srtPath
		t.ok = true
		subs = append(subs, subInput{path: srtPath, lang: t.outLang})
		audioOrdinals = append(audioOrdinals, t.ordinal)
	}

	if len(subs) == 0 {
		job.fail(firstTrackErr(job, "no speech recognized"))
		return
	}
	if len(subs) < len(job.tracks) {
		pWarn.Printf("%s — %d of %d track(s) produced no subtitle.\n",
			job.label(), len(job.tracks)-len(subs), len(job.tracks))
	}
	if cfg.srtOnly || isAudioFile(job.src) {
		job.status = "ok"
		return
	}
	job.outPath = base + ".subbed.mkv"
	if err := buildMKV(ctx, cfg, job.src, subs, audioOrdinals, job.outPath); err != nil {
		job.fail(err)
		return
	}
	job.status = "ok"
}

// uniqueSRTPath returns "<base>.<lang>.srt", appending a counter when that name
// is already taken in this run (two tracks resolving to the same language), so
// no sidecar is ever overwritten.
func uniqueSRTPath(base, lang string, used map[string]bool) string {
	p := base + "." + lang + ".srt"
	for n := 2; used[p]; n++ {
		p = fmt.Sprintf("%s.%s.%d.srt", base, lang, n)
	}
	used[p] = true
	return p
}

func (j *fileJob) fail(err error) {
	j.status = "error"
	j.err = err
}

// ----------------------------------------------------------------------------
// Diagnostic mode
// ----------------------------------------------------------------------------

func runDiagnosticMode(ctx context.Context, cfg *AppConfig, files []string) {
	for i, f := range files {
		if ctx.Err() != nil {
			return
		}
		pInfo.Printf("[%d] %s — splitting channels...\n", i+1, baseName(f))
		pd, err := probeFile(ctx, f)
		if err != nil {
			pErr.Println(err.Error())
			continue
		}
		stream, ordinal, err := pickAudioStream(cfg, pd)
		if err != nil {
			pErr.Println(err.Error())
			continue
		}
		written, err := runDiagnostic(ctx, f, stream, ordinal)
		if err != nil {
			pErr.Println(err.Error())
			continue
		}
		pOK.Printf("Wrote %d channel file(s) next to %s.\n", len(written), baseName(f))
		for _, w := range written {
			pterm.Println(pterm.Gray("    " + baseName(w)))
		}
	}
}

// ----------------------------------------------------------------------------
// Summary
// ----------------------------------------------------------------------------

func printSummary(results []*fileJob, cancelled bool) {
	ok, fail := 0, 0
	for _, r := range results {
		if r.status == "ok" {
			ok++
		} else {
			fail++
		}
	}
	pterm.Println()
	if cancelled {
		pAbort.Printf("Stopped early. Done: %d, failed/skipped: %d.\n", ok, fail)
	} else {
		pOK.Printf("Finished. Done: %d, failed: %d.\n", ok, fail)
	}
	for _, r := range results {
		if r.status == "ok" {
			pterm.Println(pterm.Green("  OK  ") + baseName(r.src))
		} else if r.err != nil {
			pterm.Println(pterm.Red("  ERR ") + baseName(r.src) + pterm.Gray("  —  "+shortErr(r.err)))
		}
	}
}

// ----------------------------------------------------------------------------
// Exit
// ----------------------------------------------------------------------------

func waitForEnter() {
	pterm.Println()
	pterm.Print(pterm.Gray("Press Enter to exit..."))
	bufio.NewReader(os.Stdin).ReadString('\n')
}
