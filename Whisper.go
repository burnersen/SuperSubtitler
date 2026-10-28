//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ----------------------------------------------------------------------------
// Engine output (whisper / faster-whisper JSON)
// ----------------------------------------------------------------------------

type whisperJSON struct {
	Language string           `json:"language"`
	Segments []whisperSegment `json:"segments"`
}

type whisperSegment struct {
	Start float64       `json:"start"`
	End   float64       `json:"end"`
	Text  string        `json:"text"`
	Words []whisperWord `json:"words"`
}

type whisperWord struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Word  string  `json:"word"`
}

// pythonExePath and whisperDriverPath are set in initTools: the embedded Python
// interpreter and our transcribe.py driver.
var (
	pythonExePath     string
	whisperDriverPath string
)

// modelDir is the model cache passed to the engine (--model_dir). Set in main.
var modelDir string

// ----------------------------------------------------------------------------
// CLI flag names (centralised: an engine flag rename is a one-line change here)
// ----------------------------------------------------------------------------

const (
	fModel        = "--model"
	fModelDir     = "--model_dir"
	fLanguage     = "--language"
	fTask         = "--task"
	fDevice       = "--device"
	fComputeType  = "--compute_type"
	fOutputDir    = "--output_dir"
	fOutputFormat = "--output_format"
	fWordTS       = "--word_timestamps"
	fVadFilter    = "--vad_filter"
	fVadThreshold = "--vad_threshold"
	fVadPad       = "--vad_speech_pad_ms"
	fCondPrev     = "--condition_on_previous_text"
	fInitPrompt   = "--initial_prompt"
	fBeamSize     = "--beam_size"
)

func boolArg(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// runWhisper runs the engine with the configured device and transparently falls
// back to CPU once if the GPU path fails. computeType is adapted for CPU. When
// onLine is non-nil, stdout is streamed to it line by line (for live progress).
func runWhisper(ctx context.Context, baseArgs []string, inputs []string, onLine func(string)) error {
	device, compute := engineDevice(lowMemActive)

	try := func(dev, comp string) error {
		args := append([]string{}, baseArgs...)
		args = append(args, fDevice, dev, fComputeType, comp)
		args = append(args, inputs...)
		callArgs := append([]string{whisperDriverPath}, args...)
		if onLine != nil {
			return runProcessStreaming(ctx, onLine, pythonExePath, callArgs...)
		}
		_, err := runProcess(ctx, pythonExePath, callArgs...)
		return err
	}

	err := try(device, compute)
	if err == nil || ctx.Err() != nil {
		return err
	}
	if device == "cuda" && looksLikeGPUError(err) {
		pWarn.Println("GPU path failed — falling back to CPU (slower). Detail: " + shortErr(err))
		return try("cpu", "int8")
	}
	return err
}

// lowMemActive mirrors cfg.lowMem for runWhisper without threading cfg through
// every signature. Set once per run in main before processing starts.
var lowMemActive bool

func looksLikeGPUError(err error) bool {
	m := strings.ToLower(err.Error())
	for _, k := range []string{"cuda", "cublas", "cudnn", "gpu", "device", "nvidia", "out of memory"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func outputJSONPath(outDir, inputPath string) string {
	base := filepath.Base(inputPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(outDir, base+".json")
}

func readWhisperJSON(path string) (*whisperJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Whisper.go: readWhisperJSON: %w", err)
	}
	var wj whisperJSON
	if err := json.Unmarshal(data, &wj); err != nil {
		return nil, fmt.Errorf("Whisper.go: readWhisperJSON (unmarshal %s): %w", filepath.Base(path), err)
	}
	return &wj, nil
}

// ----------------------------------------------------------------------------
// Language detection (multi-segment majority vote)
// ----------------------------------------------------------------------------

// detectLanguages runs the detection model over all sample chunks in one engine
// invocation (the model loads once) and returns the language of every chunk
// that produced a result, keyed by chunk path. onLine, when set, receives the
// engine's "@DONE <name>" line after each chunk.
func detectLanguages(ctx context.Context, chunks []string, outDir string, onLine func(string)) (map[string]string, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("Whisper.go: detectLanguages (mkdir): %w", err)
	}
	baseArgs := []string{
		fModel, modelArg(appSettings.detectModel),
		fModelDir, modelDir,
		fTask, "transcribe",
		fOutputDir, outDir,
		fOutputFormat, "json",
		fWordTS, boolArg(false),
		fBeamSize, "1",
		fVadFilter, boolArg(true),
	}
	if err := runWhisper(ctx, baseArgs, chunks, onLine); err != nil {
		return nil, fmt.Errorf("Whisper.go: detectLanguages: %w", err)
	}
	langs := map[string]string{}
	for _, c := range chunks {
		wj, err := readWhisperJSON(outputJSONPath(outDir, c))
		if err != nil {
			continue
		}
		if l := normalizeLang(wj.Language); l != "" {
			langs[c] = l
		}
	}
	return langs, nil
}

// voteForChunks majority-votes the detected language over one track's chunks.
func voteForChunks(langs map[string]string, chunks []string) string {
	votes := map[string]int{}
	for _, c := range chunks {
		if l := langs[c]; l != "" {
			votes[l]++
		}
	}
	return topVote(votes)
}

func topVote(votes map[string]int) string {
	type kv struct {
		k string
		n int
	}
	var list []kv
	for k, n := range votes {
		list = append(list, kv{k, n})
	}
	if len(list) == 0 {
		return ""
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].k < list[j].k
	})
	return list[0].k
}

// ----------------------------------------------------------------------------
// Main transcription
// ----------------------------------------------------------------------------

// transcribe runs the chosen model/task on the prepared WAV and returns parsed
// word-level output. onLine, if non-nil, receives engine stdout for live progress.
func transcribe(ctx context.Context, cfg *AppConfig, wavPath, model, task, lang, outDir string, onLine func(string)) (*whisperJSON, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("Whisper.go: transcribe (mkdir): %w", err)
	}
	baseArgs := []string{
		fModel, modelArg(model),
		fModelDir, modelDir,
		fTask, task,
		fOutputDir, outDir,
		fOutputFormat, "json",
		fWordTS, boolArg(true),
		fBeamSize, strconv.Itoa(appSettings.beamSize),
		fCondPrev, boolArg(appSettings.conditionPrevious),
	}
	if lang != "" {
		baseArgs = append(baseArgs, fLanguage, lang)
	}
	if cfg.noVad {
		baseArgs = append(baseArgs, fVadFilter, boolArg(false))
	} else {
		baseArgs = append(baseArgs,
			fVadFilter, boolArg(appSettings.vadFilter),
			fVadThreshold, strconv.FormatFloat(appSettings.vadThreshold, 'f', 2, 64),
			fVadPad, strconv.Itoa(appSettings.vadSpeechPadMs),
		)
	}
	if appSettings.initialPrompt != "" {
		baseArgs = append(baseArgs, fInitPrompt, appSettings.initialPrompt)
	}
	if err := runWhisper(ctx, baseArgs, []string{wavPath}, onLine); err != nil {
		return nil, fmt.Errorf("Whisper.go: transcribe: %w", err)
	}
	wj, err := readWhisperJSON(outputJSONPath(outDir, wavPath))
	if err != nil {
		return nil, fmt.Errorf("Whisper.go: transcribe (read output): %w", err)
	}
	return wj, nil
}
