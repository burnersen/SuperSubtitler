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
const appVersion = "0.3.2"

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

// Exit codes: 0 = everything done, 1 = at least one file failed or a fatal
// error, 2 = stopped by the user before the batch was complete.
const (
	exitOK        = 0
	exitFailed    = 1
	exitCancelled = 2
)

func main() {
	os.Exit(run())
}

func run() (code int) {
	started := time.Now()
	cfg := parseArgs(os.Args[1:])
	if cfg.version {
		fmt.Println(appVersion)
		fmt.Println(runtimeSummary())
		return exitOK
	}
	// -printdefaults prints the documented default INI (for front-ends).
	if cfg.printDefaults {
		fmt.Print(defaultConfigText())
		return exitOK
	}
	// -checkconfig:FILE validates an INI without changing anything, so a
	// front-end can refuse a value that would make the next start reset it.
	if cfg.checkConfig != "" {
		if err := checkConfigFile(cfg.checkConfig); err != nil {
			fmt.Println("invalid: " + err.Error())
			return exitFailed
		}
		fmt.Println("ok")
		return exitOK
	}
	// -json must switch the channels before the very first output line.
	if cfg.json {
		enableJSONMode()
	}
	noWait = cfg.noWait || cfg.json

	defer crashCatcher(&code)

	enableAnsiConsole()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setupSignalContext(cancel)
	setupConsoleCtrlHandler(cancel)

	printHeader()
	for _, f := range cfg.unknownFlags {
		pWarn.Println("Ignoring unknown flag: " + f)
	}

	if err := initJobObject(); err != nil {
		pWarn.Println("Job object unavailable — orphan-kill guarantee weakened: " + err.Error())
	}

	loadOrCreateAppConfig()
	activeCleaner = loadCleanerConfig()
	_ = writeHelpFileIfMissing()
	lowMemActive = cfg.lowMem

	if cfg.help || (len(cfg.inputArgs) == 0 && !cfg.setup) {
		printUsage()
		waitForEnter()
		return exitOK
	}

	fatal := func(mode string, err error) int {
		pErr.Println(err.Error())
		emitError(err.Error())
		emitSummary(mode, 0, 0, 0, 0, started, false)
		waitForEnter()
		return exitFailed
	}

	if cfg.outDir != "" {
		abs, err := filepath.Abs(cfg.outDir)
		if err == nil {
			err = os.MkdirAll(abs, 0755)
		}
		if err != nil {
			return fatal("subtitle", fmt.Errorf("output folder %q is not usable: %w", cfg.outDir, err))
		}
		cfg.outDir = abs
		pInfo.Println("Output folder: " + pterm.LightCyan(abs))
	}

	// -setup: install FFmpeg, Python, the engine and the models, then stop.
	if cfg.setup {
		emitRun("setup", 0, cfg)
		if err := initTools(ctx, cfg, true); err != nil {
			return fatal("setup", err)
		}
		selfExtractSource()
		pOK.Println("Setup complete — SuperSubtitler is ready to use.")
		emitSummary("setup", 0, 0, 0, 0, started, false)
		waitForEnter()
		return exitOK
	}

	printQuickTips()

	// Auto-detect a merge job (1 video + dropped audio/subtitle files) and offer
	// to mux them into one file, before falling through to normal subtitling.
	// Merging needs FFmpeg only, so it never waits for the Python setup.
	if !cfg.split && !cfg.noMerge {
		if video, audios, subs, ok := classifyMergeInputs(cfg.inputArgs); ok {
			if askMerge(video, audios, subs, cfg.auto) {
				emitRun("merge", 1, cfg)
				if err := initTools(ctx, cfg, false); err != nil {
					return fatal("merge", err)
				}
				ok := runMergeMode(ctx, cfg, video, audios, subs)
				success, failed := 1, 0
				if !ok {
					success, failed = 0, 1
				}
				emitSummary("merge", 1, success, 0, failed, started, ctx.Err() != nil)
				waitForEnter()
				return exitCode(failed, ctx.Err() != nil)
			}
		}
	}

	mode := "subtitle"
	if cfg.split {
		mode = "split"
	}
	files, skipped := collectInputFiles(cfg, cfg.inputArgs)
	emitRun(mode, len(files), cfg)
	for _, sk := range skipped {
		emitSkip(sk.path, sk.reason)
	}
	if n := countReason(skipped, skipHasSubs); n > 0 {
		pInfo.Printf("Skipped %d file(s) that already have subtitles (use -force to redo them).\n", n)
	}
	if len(files) == 0 {
		pWarn.Println("No processable video/audio files. Drag files or folders onto the EXE.")
		emitSummary(mode, 0, 0, len(skipped), 0, started, false)
		waitForEnter()
		return exitOK
	}
	pInfo.Printf("Queued %d file(s).\n", len(files))

	if err := initTools(ctx, cfg, !cfg.split); err != nil {
		return fatal(mode, err)
	}
	selfExtractSource()

	if cfg.split {
		success, failed := runDiagnosticMode(ctx, cfg, files)
		emitSummary(mode, len(files), success, len(skipped), failed, started, ctx.Err() != nil)
		waitForEnter()
		return exitCode(failed, ctx.Err() != nil)
	}

	jobs := buildFileJobs(ctx, cfg, files)
	defer cleanupJobs(jobs)
	analyzeJobs(ctx, cfg, jobs)
	results := runPipeline(ctx, cfg, jobs)
	cancelled := ctx.Err() != nil
	success, failed := printSummary(results, cancelled)
	emitSummary(mode, len(jobs), success, len(skipped), failed, started, cancelled)
	waitForEnter()
	return exitCode(failed, cancelled)
}

func exitCode(failed int, cancelled bool) int {
	switch {
	case cancelled:
		return exitCancelled
	case failed > 0:
		return exitFailed
	}
	return exitOK
}

// ----------------------------------------------------------------------------
// Init
// ----------------------------------------------------------------------------

func crashCatcher(code *int) {
	if r := recover(); r != nil {
		pErr.Printf("Unexpected error: %v\n", r)
		fmt.Println(string(debug.Stack()))
		emitError(fmt.Sprintf("unexpected error: %v", r))
		waitForEnter()
		*code = exitFailed
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
		y("• -auto  /  -force  ") + g("        >>  ") + "never ask / redo files that already have subtitles\n" +
		y("• -out:<folder>     ") + g("        >>  ") + "write the results into another folder\n" +
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
		{pterm.LightCyan("-speakers"), "Mark speaker changes: new subtitle per voice, \"- \" lines for quick exchanges."},
		{pterm.LightCyan("-split"), "Export each audio channel as a WAV (to find the speech); skip transcription."},
		{pterm.LightCyan("-auto"), "Never ask: all tracks, detected language, merge = yes."},
		{pterm.LightCyan("-force"), "Redo files that already have subtitles (overwrites the results)."},
		{pterm.LightCyan("-out:DIR"), "Write .srt/.mkv results into DIR instead of next to the source."},
		{pterm.LightCyan("-nowait"), "Do not wait for Enter at the end."},
		{pterm.LightCyan("-setup"), "Download and install everything (incl. speech models), then exit."},
		{pterm.LightCyan("-nomerge"), "Never offer the merge mode; subtitle every dropped file."},
		{pterm.LightCyan("-json"), "Machine-readable events on stdout (for front-ends); implies -nowait."},
		{pterm.LightCyan("-version"), "Print the version and exit."},
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
	return pterm.Gray("  · ETA " + fmtDuration(time.Duration(etaSeconds(start, frac))*time.Second))
}

// etaSeconds estimates the remaining seconds from elapsed time and progress;
// 0 while too little is known to say anything honest.
func etaSeconds(start time.Time, frac float64) float64 {
	if frac <= 0.02 {
		return 0
	}
	remain := time.Since(start).Seconds() * (1 - frac) / frac
	if remain < 0 {
		return 0
	}
	return remain
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

// initTools makes sure FFmpeg is present and, when needWhisper is set, the
// embedded Python, the engine and the speech models this run will use. Merge
// and split need FFmpeg only and never wait for the (large) Python setup.
func initTools(ctx context.Context, cfg *AppConfig, needWhisper bool) error {
	dir := exeDir()
	ffmpegPath = filepath.Join(dir, "ffmpeg.exe")
	ffprobePath = filepath.Join(dir, "ffprobe.exe")

	if !ffmpegIsPinned(dir) {
		if fileExists(ffmpegPath) {
			pInfo.Println("Replacing FFmpeg with the tested build " + pins.FFmpeg.Version + " (one-time)...")
		} else {
			pInfo.Println("FFmpeg not found next to the EXE — downloading once...")
		}
		if err := downloadFFmpeg(ctx, dir); err != nil {
			return err
		}
	}
	if !needWhisper {
		emitSetup("ready", "done", "", 0, 0, 0)
		return nil
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
	if err := ensureModels(ctx, modelsNeeded(cfg)); err != nil {
		return err
	}
	emitSetup("ready", "done", "", 0, 0, 0)
	return nil
}

// modelsNeeded lists the speech models a run will load, de-duplicated, so they
// can be fetched up front with visible progress instead of silently inside the
// first transcription.
func modelsNeeded(cfg *AppConfig) []string {
	var list []string
	add := func(m string) {
		for _, x := range list {
			if x == m {
				return
			}
		}
		list = append(list, m)
	}
	if cfg.forcedLang == "" || cfg.setup {
		add(appSettings.detectModel)
	}
	if cfg.translate {
		add(appSettings.modelTranslate)
	} else {
		add(appSettings.modelTranscribe)
	}
	if cfg.speakers || cfg.setup {
		add(speakerSegmentationModel)
	}
	return list
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
		case strings.HasPrefix(low, "-out:"):
			cfg.outDir = strings.Trim(a[len("-out:"):], `"`)
		case low == "-translate" || low == "-en":
			cfg.translate = true
		case low == "-speakers":
			cfg.speakers = true
		case strings.HasPrefix(low, "-speakers:"):
			cfg.speakers = true // 0.3.x took a number of speakers; no longer needed
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
		case low == "-auto" || low == "-yes":
			cfg.auto = true
		case low == "-force":
			cfg.force = true
		case low == "-nowait":
			cfg.noWait = true
		case low == "-json":
			cfg.json = true
		case low == "-setup":
			cfg.setup = true
		case low == "-nomerge":
			cfg.noMerge = true
		case low == "-printdefaults":
			cfg.printDefaults = true
		case strings.HasPrefix(low, "-checkconfig:"):
			cfg.checkConfig = strings.Trim(a[len("-checkconfig:"):], `"`)
		case low == "-version" || low == "--version":
			cfg.version = true
		case low == "-help" || low == "-h" || low == "-?" || low == "--help":
			cfg.help = true
		case strings.HasPrefix(a, "-"):
			cfg.unknownFlags = append(cfg.unknownFlags, a)
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

// skipped input reasons (also the "reason" of the JSON skip event).
const (
	skipHasSubs   = "has_subs"
	skipOwnOutput = "own_output"
)

type skippedInput struct {
	path   string
	reason string
}

func countReason(list []skippedInput, reason string) int {
	n := 0
	for _, s := range list {
		if s.reason == reason {
			n++
		}
	}
	return n
}

func collectInputFiles(cfg *AppConfig, args []string) ([]string, []skippedInput) {
	seen := map[string]bool{}
	var files []string
	var skipped []skippedInput

	consider := func(path string) {
		ext := strings.ToLower(filepath.Ext(path))
		if !videoExtensions[ext] && !audioExtensions[ext] {
			return
		}
		if isOwnOutput(path) {
			skipped = append(skipped, skippedInput{path, skipOwnOutput}) // never re-feed
			return
		}
		// Existing subtitles only matter when subtitling; a channel export
		// (-split) of an already subtitled film is a perfectly normal request.
		if !cfg.force && !cfg.split && hasExistingSubs(outputBase(cfg, path)) {
			skipped = append(skipped, skippedInput{path, skipHasSubs})
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

// isOwnOutput reports files this tool wrote itself (.subbed.mkv, .joined.mkv),
// which must never be fed back in as input.
func isOwnOutput(path string) bool {
	low := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(low, ".subbed.mkv") {
		return true
	}
	// "<name>.joined.mkv" or numbered "<name>.joined.2.mkv"
	stem := strings.TrimSuffix(low, ".mkv")
	if i := strings.LastIndex(stem, ".joined"); i >= 0 {
		rest := stem[i+len(".joined"):]
		if rest == "" {
			return true
		}
		if rest[0] == '.' {
			if _, err := strconv.Atoi(rest[1:]); err == nil {
				return true
			}
		}
	}
	return false
}

// outputBase is the path stem every result of src is named after: next to the
// source by default, or inside the -out folder.
func outputBase(cfg *AppConfig, src string) string {
	stem := strings.TrimSuffix(src, filepath.Ext(src))
	if cfg.outDir == "" {
		return stem
	}
	return filepath.Join(cfg.outDir, filepath.Base(stem))
}

// hasExistingSubs reports whether a sidecar .srt or a .subbed.mkv already exists
// for this output stem, so a re-scan skips finished work.
func hasExistingSubs(base string) bool {
	if fileExists(base + ".subbed.mkv") {
		return true
	}
	if m, _ := filepath.Glob(globEscape(base) + ".*.srt"); len(m) > 0 {
		return true
	}
	return false
}

// globEscape protects glob metacharacters in a literal path ("Movie [1080p]"
// would otherwise be read as a character class and never match).
func globEscape(p string) string {
	r := strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]")
	return r.Replace(p)
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
	detected  string // detection vote, "" when skipped (-lang) or inconclusive
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
		jobs = append(jobs, job)
		emitStage(i+1, 0, "probe")
		pd, err := probeFile(ctx, f)
		if err != nil {
			job.fail(err)
			emitFile(job, len(files))
			continue
		}
		job.duration = pd.durationSec()
		if len(pd.audioStreams()) > 1 && cfg.forcedTrack <= 0 {
			pInfo.Printf("%s\n", job.label()) // context for the upcoming question
		}
		sels, err := selectTracks(cfg, pd, f)
		if err != nil {
			job.fail(err)
			emitFile(job, len(files))
			continue
		}
		for _, s := range sels {
			job.tracks = append(job.tracks, &trackJob{stream: s.stream, ordinal: s.ordinal})
		}
		emitFile(job, len(files))
	}
	return jobs
}

// cleanupJobs removes every work folder that is still around — a cancelled
// batch never reaches finalizeStage for the files it did not start.
func cleanupJobs(jobs []*fileJob) {
	for _, j := range jobs {
		if j.work != "" {
			_ = os.RemoveAll(j.work)
			j.work = ""
		}
	}
}

// analyzeJobs settles the language of every selected track BEFORE the long
// unattended part begins, so every question (language mismatch, unknown
// language) is asked while the user is still at the screen — never at file 7
// in the middle of the night.
//
// Detection samples of all tracks are extracted first and then detected in ONE
// engine call, so the detection model loads once for the whole batch instead
// of once per file. If that shared call fails, each track is retried on its own
// so one broken file cannot take the others' detection down with it.
func analyzeJobs(ctx context.Context, cfg *AppConfig, jobs []*fileJob) {
	type pending struct {
		job   *fileJob
		track int
		t     *trackJob
	}
	var todo []pending
	var allChunks []string

	detDir := ""
	if cfg.forcedLang == "" {
		d, err := os.MkdirTemp("", "subs-detect-*")
		if err != nil {
			pErr.Println("Cannot create a temp folder for language detection: " + err.Error())
		} else {
			detDir = d
			defer os.RemoveAll(detDir)
		}
	}

	for _, job := range jobs {
		if job.err != nil {
			continue
		}
		if ctx.Err() != nil {
			job.fail(ctx.Err())
			continue
		}
		work, err := os.MkdirTemp("", "subs-*")
		if err != nil {
			job.fail(fmt.Errorf("create work dir: %w", err))
			continue
		}
		job.work = work
		if cfg.forcedLang != "" || detDir == "" {
			continue
		}
		for i, t := range job.tracks {
			emitStage(job.index+1, i+1, "detect")
			prefix := fmt.Sprintf("j%03dt%02d_", job.index, i)
			chunks, err := extractDetectionChunks(ctx, cfg, job.src, t.stream, t.ordinal, job.duration, detDir, prefix)
			if err != nil {
				t.err = err
				continue
			}
			t.detChunks = chunks
			allChunks = append(allChunks, chunks...)
			todo = append(todo, pending{job, i, t})
		}
	}

	if len(allChunks) > 0 && ctx.Err() == nil {
		outDir := filepath.Join(detDir, "out")
		spinner, _ := pterm.DefaultSpinner.WithText(fmt.Sprintf("Detecting the spoken language of %d track(s)...", len(todo))).Start()
		done := 0
		onLine := func(line string) {
			if strings.HasPrefix(line, "@DONE ") {
				done++
				if spinner != nil {
					spinner.UpdateText(fmt.Sprintf("Detecting the spoken language... (%d/%d samples)", done, len(allChunks)))
				}
			}
		}
		votes, err := detectLanguages(ctx, allChunks, outDir, onLine)
		if err != nil && ctx.Err() == nil && len(todo) > 1 {
			pWarn.Println("Shared language detection failed — retrying file by file: " + shortErr(err))
			votes = map[string]string{}
			for _, p := range todo {
				v, perr := detectLanguages(ctx, p.t.detChunks, filepath.Join(outDir, fmt.Sprintf("r%03d_%02d", p.job.index, p.track)), nil)
				if perr != nil {
					p.t.err = perr
					continue
				}
				for k, l := range v {
					votes[k] = l
				}
			}
			err = nil
		}
		if spinner != nil {
			_ = spinner.Stop()
		}
		for _, p := range todo {
			if err != nil {
				p.t.err = err
				continue
			}
			p.t.detected = voteForChunks(votes, p.t.detChunks)
		}
	}

	// Resolve the plan of every track — this is where questions may come up.
	for _, job := range jobs {
		if job.err != nil {
			continue
		}
		if ctx.Err() != nil {
			job.fail(ctx.Err())
			continue
		}
		for i, t := range job.tracks {
			if t.err != nil {
				continue
			}
			tag := containerLang(t.stream)
			if willAskLanguage(cfg, tag, t.detected) {
				pInfo.Printf("%s\n", trackLabelFor(job, i)) // context for the upcoming question
			}
			src := decideLanguage(cfg, tag, t.detected, job.src)
			t.plan = buildPlan(cfg, src)
			t.outLang = t.plan.outLang
			emitEvent(eventLang{
				Ev: "lang", Index: job.index + 1, Track: i + 1,
				Detected: t.detected, Tag: tag, Src: t.plan.srcLang, Out: t.plan.outLang,
				Task: t.plan.task, Model: t.plan.model,
			})
			pInfo.Printf("%s — %s\n", trackLabelFor(job, i), describePlan(t, tag))
		}
		if aliveTracks(job) == 0 {
			job.fail(firstTrackErr(job, "language detection failed for all tracks"))
		}
	}
}

// willAskLanguage mirrors decideLanguage: true when it is going to ask.
func willAskLanguage(cfg *AppConfig, tag, detected string) bool {
	if cfg.forcedLang != "" || cfg.auto || cfg.translate {
		return false
	}
	tag, detected = normalizeLang(tag), normalizeLang(detected)
	return (tag != "" && detected != "" && tag != detected) || (tag == "" && detected == "")
}

// trackLabelFor names a file, or one of its tracks when it has several.
func trackLabelFor(job *fileJob, i int) string {
	if len(job.tracks) > 1 {
		return fmt.Sprintf("%s (track %d/%d)", job.label(), i+1, len(job.tracks))
	}
	return job.label()
}

// describePlan renders the resolved language decision in one short line.
func describePlan(t *trackJob, tag string) string {
	var how []string
	if t.detected != "" {
		how = append(how, "detected "+t.detected)
	}
	if tag != "" {
		how = append(how, "tag "+tag)
	}
	line := "language: " + t.plan.outLang
	if t.plan.task == "translate" {
		src := t.plan.srcLang
		if src == "" {
			src = "auto"
		}
		line = "translate " + src + " → en"
	}
	if len(how) > 0 {
		line += pterm.Gray("  (" + strings.Join(how, ", ") + ")")
	}
	return line
}

func aliveTracks(job *fileJob) int {
	n := 0
	for _, t := range job.tracks {
		if t.err == nil {
			n++
		}
	}
	return n
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
			emitResult(job)
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
		return // already failed during probing/selection/analysis
	}
	if ctx.Err() != nil {
		job.fail(ctx.Err())
		return
	}
	alive := 0
	for i, t := range job.tracks {
		if t.err != nil {
			continue
		}
		if ctx.Err() != nil {
			job.fail(ctx.Err())
			return
		}
		emitStage(job.index+1, i+1, "extract")
		tdir := filepath.Join(job.work, fmt.Sprintf("t%d", i))
		if err := os.MkdirAll(tdir, 0755); err != nil {
			t.err = fmt.Errorf("create track dir: %w", err)
			continue
		}
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
	for i, t := range job.tracks {
		if t.err != nil || t.mainWav == "" {
			continue // analysis or extraction already failed for this track
		}
		if ctx.Err() != nil {
			job.fail(ctx.Err())
			return
		}
		tdir := filepath.Join(job.work, fmt.Sprintf("t%d", i))
		tlabel := trackLabelFor(job, i)
		emitStage(job.index+1, i+1, "transcribe")

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
		lastEmit := -1.0
		onLine := func(line string) {
			f, ok := parseProgress(line)
			if !ok {
				return
			}
			if f-lastEmit >= 0.002 || f >= 1 {
				lastEmit = f
				emitEvent(eventProgress{
					Ev: "progress", Index: job.index + 1, Track: i + 1, Tracks: len(job.tracks),
					Pct: f, ETASec: etaSeconds(started, f),
				})
			}
			if bar == nil {
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
	base := outputBase(cfg, job.src)
	usedSRT := map[string]bool{}

	var subs []subInput
	var audioOrdinals []int
	for i, t := range job.tracks {
		if t.wj == nil {
			continue // track failed earlier
		}
		var turns []speakerTurn
		if cfg.speakers {
			turns = markSpeakers(ctx, job, i, t)
			if ctx.Err() != nil {
				job.fail(ctx.Err())
				return
			}
		}
		srt := buildSRT(t.wj, leadinSec, appSettings, turns)
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
	emitStage(job.index+1, 0, "mux")
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

func runDiagnosticMode(ctx context.Context, cfg *AppConfig, files []string) (success, failed int) {
	for i, f := range files {
		if ctx.Err() != nil {
			return
		}
		emitEvent(eventFile{Ev: "file", Index: i + 1, Total: len(files), Name: baseName(f), Path: f})
		emitStage(i+1, 0, "extract")
		res := eventResult{Ev: "result", Index: i + 1, Name: baseName(f), Status: "failed"}
		fail := func(err error) {
			pErr.Println(err.Error())
			res.Message = shortErr(err)
			emitEvent(res)
			failed++
		}
		pInfo.Printf("[%d] %s — splitting channels...\n", i+1, baseName(f))
		pd, err := probeFile(ctx, f)
		if err != nil {
			fail(err)
			continue
		}
		stream, ordinal, err := pickAudioStream(cfg, pd)
		if err != nil {
			fail(err)
			continue
		}
		written, err := runDiagnostic(ctx, f, outputBase(cfg, f), stream, ordinal)
		if err != nil {
			fail(err)
			continue
		}
		pOK.Printf("Wrote %d channel file(s) for %s.\n", len(written), baseName(f))
		for _, w := range written {
			pterm.Println(pterm.Gray("    " + baseName(w)))
		}
		res.Status, res.Outputs = "success", written
		emitEvent(res)
		success++
	}
	return
}

// ----------------------------------------------------------------------------
// Summary
// ----------------------------------------------------------------------------

func printSummary(results []*fileJob, cancelled bool) (ok, fail int) {
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
	return ok, fail
}

// ----------------------------------------------------------------------------
// Exit
// ----------------------------------------------------------------------------

// noWait skips the final "Press Enter" (-nowait, and always with -json).
var noWait bool

func waitForEnter() {
	if noWait {
		return
	}
	pterm.Println()
	pterm.Print(pterm.Gray("Press Enter to exit..."))
	_, _ = readAnswerLine()
}
