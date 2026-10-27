//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pterm/pterm"
)

const configFileName = "SuperSubtitler_Config.ini"

// AppSettings holds everything loaded from the INI. Populated once in main via
// loadOrCreateAppConfig and read-only afterwards.
type AppSettings struct {
	// Language detection
	detectSeconds      int    // length of the analysis sample
	detectStartPercent int    // where in the file the sample starts
	detectModel        string // model used only for detection

	// Model / task policy
	modelTranscribe string // transcribe in the source's own language (fast, multilingual)
	modelTranslate  string // -translate path (English output); turbo cannot translate

	// Engine runtime
	device            string // "cuda" (GPU, CPU fallback) or "cpu"
	computeType       string // float16 on GPU
	computeTypeLowMem string // int8 etc. for -lowmem
	beamSize          int

	// Python runtime
	pythonVersion string // embeddable Python version to fetch (X.Y.Z)
	engineSpec    string // pip spec for the engine, e.g. "faster-whisper" or "faster-whisper==1.0.3"
	installCUDA   bool   // pip-install the CUDA wheels so the GPU path works out of the box

	// VAD / anti-hallucination
	vadFilter         bool
	vadThreshold      float64
	vadSpeechPadMs    int
	conditionPrevious bool   // keep false to break repetition loops
	initialPrompt     string // optional steering prompt

	// Audio
	leadinMs     int    // silence prepended so the model gets a run-up
	surroundMode string // "center_emphasis" or "pure_center"

	// Subtitle formatting
	subMaxCharsPerLine int
	subMaxLines        int
	subMaxCPS          float64
	subMinDurMs        int
	subMaxDurMs        int
	subMinGapMs        int
	subGapSplitMs      int
}

// AppConfig holds per-run flag state. Populated once in main, then passed by
// value/pointer; no global mutation.
type AppConfig struct {
	forcedTrack   int    // 1-based audio stream, 0 = auto
	forcedChannel int    // 0-based physical channel, -1 = auto
	forcedLang    string // ISO code, "" = auto
	translate     bool   // -translate: translate everything to English
	split         bool   // -split: export each audio channel as WAV, skip transcription
	srtOnly       bool   // -srtonly: write only the .srt, no MKV
	lean          bool   // -lean: drop foreign streams, keep video + chosen audio
	noVad         bool   // -novad: disable the silence filter
	lowMem        bool   // -lowmem: smaller compute type
	auto          bool   // -auto: never ask, take every default answer
	force         bool   // -force: redo files that already have subtitles
	outDir        string // -out:<dir>: write results there instead of next to the source
	noWait        bool   // -nowait: no "Press Enter" at the end
	json          bool   // -json: machine-readable events on stdout
	setup         bool   // -setup: install everything (incl. models), then exit
	noMerge       bool   // -nomerge: never offer merge mode, subtitle every input
	checkConfig   string // -checkconfig:FILE: validate an INI, print ok/invalid, exit
	printDefaults bool   // -printdefaults: print the default INI, exit
	version       bool   // -version: print the version and exit
	help          bool   // -help: show usage and exit
	unknownFlags  []string
	inputArgs     []string
}

func defaultSettings() AppSettings {
	return AppSettings{
		detectSeconds:      180,
		detectStartPercent: 10,
		detectModel:        "large-v3-turbo",
		modelTranscribe:    "large-v3-turbo",
		modelTranslate:     "large-v3",
		device:             "cuda",
		computeType:        "float16",
		computeTypeLowMem:  "int8",
		beamSize:           5,
		pythonVersion:      "3.12.10",
		engineSpec:         defaultEngineSpec,
		installCUDA:        true,
		vadFilter:          true,
		vadThreshold:       0.50,
		vadSpeechPadMs:     400,
		conditionPrevious:  false,
		initialPrompt:      "",
		leadinMs:           200,
		surroundMode:       "center_emphasis",
		subMaxCharsPerLine: 42,
		subMaxLines:        2,
		subMaxCPS:          17.0,
		subMinDurMs:        833,
		subMaxDurMs:        7000,
		subMinGapMs:        83,
		subGapSplitMs:      500,
	}
}

var appSettings = defaultSettings()

// defaultEngineSpec means "the tested, pinned package set from runtime.json".
// Any other engine_spec is installed as written, newest versions, unpinned.
const defaultEngineSpec = "faster-whisper"

func configPath() string {
	exe, err := os.Executable()
	if err != nil {
		return configFileName
	}
	return filepath.Join(filepath.Dir(exe), configFileName)
}

// loadOrCreateAppConfig loads the INI, creating it with defaults on first run.
// If any value is out of range the whole file is reset to defaults, matching the
// behaviour of the sibling tools.
func loadOrCreateAppConfig() {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		writeDefaultConfig(path)
		return
	}
	s := parseSettings(string(data))
	if !validateSettings(s) {
		pWarn.Println("Invalid value in " + configFileName + " — file reset to defaults.")
		writeDefaultConfig(path)
		return
	}
	appSettings = s
}

func applySetting(s *AppSettings, key, val string) {
	atoi := func(def int) int {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
		return def
	}
	atof := func(def float64) float64 {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return def
	}
	atob := func(def bool) bool {
		switch strings.ToLower(val) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
		return def
	}
	switch key {
	case "detect_seconds":
		s.detectSeconds = atoi(s.detectSeconds)
	case "detect_start_percent":
		s.detectStartPercent = atoi(s.detectStartPercent)
	case "detect_model":
		s.detectModel = val
	case "model_transcribe":
		s.modelTranscribe = val
	case "model_translate":
		s.modelTranslate = val
	case "device":
		s.device = strings.ToLower(val)
	case "compute_type":
		s.computeType = val
	case "compute_type_lowmem":
		s.computeTypeLowMem = val
	case "beam_size":
		s.beamSize = atoi(s.beamSize)
	case "python_version":
		s.pythonVersion = val
	case "engine_spec":
		s.engineSpec = val
	case "install_cuda":
		s.installCUDA = atob(s.installCUDA)
	case "vad_filter":
		s.vadFilter = atob(s.vadFilter)
	case "vad_threshold":
		s.vadThreshold = atof(s.vadThreshold)
	case "vad_speech_pad_ms":
		s.vadSpeechPadMs = atoi(s.vadSpeechPadMs)
	case "condition_on_previous_text":
		s.conditionPrevious = atob(s.conditionPrevious)
	case "initial_prompt":
		s.initialPrompt = val
	case "leadin_ms":
		s.leadinMs = atoi(s.leadinMs)
	case "surround_mode":
		s.surroundMode = strings.ToLower(val)
	case "sub_max_chars_per_line":
		s.subMaxCharsPerLine = atoi(s.subMaxCharsPerLine)
	case "sub_max_lines":
		s.subMaxLines = atoi(s.subMaxLines)
	case "sub_max_cps":
		s.subMaxCPS = atof(s.subMaxCPS)
	case "sub_min_dur_ms":
		s.subMinDurMs = atoi(s.subMinDurMs)
	case "sub_max_dur_ms":
		s.subMaxDurMs = atoi(s.subMaxDurMs)
	case "sub_min_gap_ms":
		s.subMinGapMs = atoi(s.subMinGapMs)
	case "sub_gap_split_ms":
		s.subGapSplitMs = atoi(s.subGapSplitMs)
	}
}

func validateSettings(s AppSettings) bool {
	return settingsProblem(s) == ""
}

// settingsProblem names the first out-of-range value, or "" when all are valid.
// The text is shown by -checkconfig, so a front-end can refuse to save a value
// that would otherwise make the next start reset the whole file.
func settingsProblem(s AppSettings) string {
	switch {
	case s.detectSeconds < 30 || s.detectSeconds > 1800:
		return "detect_seconds must be 30..1800"
	case s.detectStartPercent < 0 || s.detectStartPercent > 90:
		return "detect_start_percent must be 0..90"
	case s.device != "cuda" && s.device != "cpu":
		return "device must be cuda or cpu"
	case s.surroundMode != "center_emphasis" && s.surroundMode != "pure_center":
		return "surround_mode must be center_emphasis or pure_center"
	case s.beamSize < 1 || s.beamSize > 10:
		return "beam_size must be 1..10"
	case s.vadThreshold < 0 || s.vadThreshold > 1:
		return "vad_threshold must be 0..1"
	case s.vadSpeechPadMs < 0 || s.vadSpeechPadMs > 2000:
		return "vad_speech_pad_ms must be 0..2000"
	case s.leadinMs < 0 || s.leadinMs > 5000:
		return "leadin_ms must be 0..5000"
	case s.subMaxCharsPerLine < 20 || s.subMaxCharsPerLine > 80:
		return "sub_max_chars_per_line must be 20..80"
	case s.subMaxLines < 1 || s.subMaxLines > 3:
		return "sub_max_lines must be 1..3"
	case s.subMaxCPS < 5 || s.subMaxCPS > 40:
		return "sub_max_cps must be 5..40"
	case s.subMinDurMs < 200 || s.subMinDurMs > s.subMaxDurMs:
		return "sub_min_dur_ms must be at least 200 and not above sub_max_dur_ms"
	case s.subMaxDurMs < 1000 || s.subMaxDurMs > 20000:
		return "sub_max_dur_ms must be 1000..20000"
	case s.subMinGapMs < 0 || s.subMinGapMs > 1000:
		return "sub_min_gap_ms must be 0..1000"
	case s.subGapSplitMs < 100 || s.subGapSplitMs > 5000:
		return "sub_gap_split_ms must be 100..5000"
	case s.detectModel == "" || s.modelTranscribe == "" || s.modelTranslate == "" || s.computeType == "":
		return "detect_model, model_transcribe, model_translate and compute_type must not be empty"
	case s.engineSpec == "" || strings.Count(s.pythonVersion, ".") < 2:
		return "engine_spec must not be empty and python_version must be X.Y.Z"
	}
	return ""
}

// parseSettings reads INI text over the defaults. Unknown keys are ignored.
func parseSettings(text string) AppSettings {
	s := defaultSettings()
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		applySetting(&s, strings.TrimSpace(key), strings.TrimSpace(val))
	}
	return s
}

// checkConfigFile validates an INI file without touching it (-checkconfig).
func checkConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if p := settingsProblem(parseSettings(string(data))); p != "" {
		return errors.New(p)
	}
	return nil
}

// writeDefaultConfig writes the documented default INI and makes it active.
func writeDefaultConfig(path string) {
	if err := os.WriteFile(path, []byte(defaultConfigText()), 0644); err != nil {
		pWarn.Printf("Could not write %s: %v\n", configFileName, err)
		return
	}
	appSettings = defaultSettings()
	pInfo.Println("Created " + pterm.LightCyan(configFileName) + " with default settings.")
}

// defaultConfigText is the complete default INI (-printdefaults prints it, so
// a front-end can show every default without keeping its own copy).
func defaultConfigText() string {
	d := defaultSettings()
	return fmt.Sprintf(`# SuperSubtitler configuration
# Delete this file to regenerate defaults. Invalid values reset the whole file.

[detection]
# Length and start offset of the audio sample used to detect the language.
# A longer sample is split into 30s windows and majority-voted, which raises
# the chance of catching the correct language on mixed/noisy material.
detect_seconds=%d
detect_start_percent=%d
detect_model=%s

[models]
# model_transcribe is used for EVERY language (turbo: fast, multilingual).
# model_translate is used only for -translate (English output); it must be a
# large model — turbo cannot translate. large-v3-turbo and large-v3 are pinned
# to an exact, verified revision (from this project's mirror first); any other
# name is fetched as its source currently offers it.
model_transcribe=%s
model_translate=%s

[engine]
device=%s
compute_type=%s
compute_type_lowmem=%s
beam_size=%d

[python]
# With the defaults (python_version as shipped, engine_spec=faster-whisper)
# SuperSubtitler installs its tested set: exact versions of Python, pip, the
# engine, every package and the CUDA libraries, each checked against its
# SHA-256 (built into the EXE). Change either value only if you know why:
# then the newest versions are installed instead, NOT the tested set (pin
# like faster-whisper==1.0.3 to freeze one yourself). install_cuda pulls the
# CUDA libraries so the GPU works without a system CUDA toolkit. Delete the
# Python\ folder to rebuild from scratch.
python_version=%s
engine_spec=%s
install_cuda=%t

[vad]
# Silence filter. Lower threshold = less aggressive (keeps quiet speech).
# vad_speech_pad_ms is the time cushion kept around detected speech.
vad_filter=%t
vad_threshold=%.2f
vad_speech_pad_ms=%d

[robustness]
# Keep condition_on_previous_text=false to break endless repetition loops.
condition_on_previous_text=%t
initial_prompt=%s

[audio]
# Silence prepended so the model does not swallow the first word; subtracted
# again from the final timings. surround_mode: center_emphasis | pure_center
leadin_ms=%d
surround_mode=%s

[subtitles]
sub_max_chars_per_line=%d
sub_max_lines=%d
sub_max_cps=%.1f
sub_min_dur_ms=%d
sub_max_dur_ms=%d
sub_min_gap_ms=%d
sub_gap_split_ms=%d
`,
		d.detectSeconds, d.detectStartPercent, d.detectModel,
		d.modelTranscribe, d.modelTranslate,
		d.device, d.computeType, d.computeTypeLowMem, d.beamSize,
		d.pythonVersion, d.engineSpec, d.installCUDA,
		d.vadFilter, d.vadThreshold, d.vadSpeechPadMs,
		d.conditionPrevious, d.initialPrompt,
		d.leadinMs, d.surroundMode,
		d.subMaxCharsPerLine, d.subMaxLines, d.subMaxCPS,
		d.subMinDurMs, d.subMaxDurMs, d.subMinGapMs, d.subGapSplitMs,
	)
}
