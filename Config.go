//go:build windows && amd64

package main

import (
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
	device           string // "cuda" (GPU, CPU fallback) or "cpu"
	computeType      string // float16 on GPU
	computeTypeLowMem string // int8 etc. for -lowmem
	beamSize         int

	// Python runtime
	pythonVersion string // embeddable Python version to fetch (X.Y.Z)
	engineSpec    string // pip spec for the engine, e.g. "faster-whisper" or "faster-whisper==1.0.3"
	installCUDA   bool   // pip-install the CUDA wheels so the GPU path works out of the box

	// VAD / anti-hallucination
	vadFilter        bool
	vadThreshold     float64
	vadSpeechPadMs   int
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
	help          bool   // -help: show usage and exit
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
		engineSpec:         "faster-whisper",
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
	s := defaultSettings()
	for _, raw := range strings.Split(string(data), "\n") {
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
	if s.detectSeconds < 30 || s.detectSeconds > 1800 {
		return false
	}
	if s.detectStartPercent < 0 || s.detectStartPercent > 90 {
		return false
	}
	if s.device != "cuda" && s.device != "cpu" {
		return false
	}
	if s.surroundMode != "center_emphasis" && s.surroundMode != "pure_center" {
		return false
	}
	if s.beamSize < 1 || s.beamSize > 10 {
		return false
	}
	if s.vadThreshold < 0 || s.vadThreshold > 1 {
		return false
	}
	if s.leadinMs < 0 || s.leadinMs > 5000 {
		return false
	}
	if s.subMaxCharsPerLine < 20 || s.subMaxCharsPerLine > 80 {
		return false
	}
	if s.subMaxLines < 1 || s.subMaxLines > 3 {
		return false
	}
	if s.subMaxCPS < 5 || s.subMaxCPS > 40 {
		return false
	}
	if s.subMinDurMs < 200 || s.subMinDurMs > s.subMaxDurMs {
		return false
	}
	if s.subMaxDurMs < 1000 || s.subMaxDurMs > 20000 {
		return false
	}
	if s.detectModel == "" || s.modelTranscribe == "" || s.modelTranslate == "" || s.computeType == "" {
		return false
	}
	if s.engineSpec == "" || strings.Count(s.pythonVersion, ".") < 2 {
		return false
	}
	return true
}

func writeDefaultConfig(path string) {
	d := defaultSettings()
	content := fmt.Sprintf(`# SuperSubtitler configuration
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
# large model — turbo cannot translate.
model_transcribe=%s
model_translate=%s

[engine]
device=%s
compute_type=%s
compute_type_lowmem=%s
beam_size=%d

[python]
# Embedded Python runtime. python_version must be a full X.Y.Z that python.org
# ships as an "embed-amd64" build. engine_spec is the pip package (pin it like
# faster-whisper==1.0.3 to freeze a version). install_cuda pulls the CUDA wheels
# so the GPU works without a system CUDA toolkit. Changing engine_spec re-runs
# pip on the next start; delete the Python\ folder to rebuild from scratch.
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		pWarn.Printf("Could not write %s: %v\n", configFileName, err)
		return
	}
	appSettings = d
	pInfo.Println("Created " + pterm.LightCyan(configFileName) + " with default settings.")
}
