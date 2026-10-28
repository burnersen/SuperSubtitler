//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// Speakers.go — speaker marking (-speakers).
//
// After a track is transcribed, sherpa-onnx tells its voices apart on the
// processor (transcribe.py --diarize, a process of its own) while the graphics
// card already works on the next file. The result is a list of turns — who
// speaks when — which Subtitle.go uses to start a new subtitle when the voice
// changes and to put two short lines of different speakers into one subtitle
// as "- " lines. If it fails, the subtitle is written without marks: a missing
// mark never costs the subtitle itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The pinned models (runtime.json): pyannote segmentation 3.0 finds where
// speech and speaker changes are, the 3D-Speaker ERes2Net embedding tells the
// voices apart. Chosen with the speaker benchmark (.github/workflows/
// speaker-benchmark.yml) out of six embeddings and many thresholds: 10.75 %
// diarization error at speaker_threshold 0.95, where the first choice
// (CAM++ at 0.5) had 44 %.
const (
	speakerSegmentationModel = "speaker-segmentation"
	speakerEmbeddingModel    = "speaker-embedding"
)

// speakerTurn is one stretch of one voice, in the WAV's timeline (that is,
// including the lead-in, like the engine's word times). Speakers are numbered
// in order of appearance from 0.
type speakerTurn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker int     `json:"speaker"`
}

// speakerModelFile is the .onnx file of an installed speaker model.
func speakerModelFile(name string) (string, error) {
	dir := modelArg(name)
	for _, f := range pins.Models[name].Files {
		if strings.HasSuffix(f.Name, ".onnx") {
			return filepath.Join(dir, f.Name), nil
		}
	}
	return "", fmt.Errorf("Speakers.go: %s has no .onnx file", name)
}

// speakerCountFor is the fixed number of speakers for this run, 0 = find out.
func speakerCountFor(cfg *AppConfig) int {
	if cfg.speakerCount > 0 {
		return cfg.speakerCount
	}
	return appSettings.speakerCount
}

// diarize runs the speaker recognition over wavPath. onLine receives the
// driver's "@PROGRESS x" lines.
func diarize(ctx context.Context, cfg *AppConfig, wavPath, outPath string, onLine func(string)) ([]speakerTurn, error) {
	seg, err := speakerModelFile(speakerSegmentationModel)
	if err != nil {
		return nil, err
	}
	emb, err := speakerModelFile(speakerEmbeddingModel)
	if err != nil {
		return nil, err
	}
	args := []string{whisperDriverPath, "--diarize",
		"--segmentation", seg,
		"--embedding", emb,
		"--threshold", strconv.FormatFloat(appSettings.speakerThreshold, 'f', 2, 64),
		"--num_speakers", strconv.Itoa(speakerCountFor(cfg)),
		"--output", outPath,
		wavPath,
	}
	if err := runProcessStreaming(ctx, onLine, pythonExePath, args...); err != nil {
		return nil, fmt.Errorf("Speakers.go: diarize: %w", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("Speakers.go: diarize (read result): %w", err)
	}
	return parseTurns(data)
}

func parseTurns(data []byte) ([]speakerTurn, error) {
	var out struct {
		Turns []speakerTurn `json:"turns"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("Speakers.go: parseTurns: %w", err)
	}
	return out.Turns, nil
}

// countSpeakers is the number of different voices in turns.
func countSpeakers(turns []speakerTurn) int {
	seen := map[int]bool{}
	for _, t := range turns {
		seen[t.Speaker] = true
	}
	return len(seen)
}

// noSpeaker marks a word no turn covers.
const noSpeaker = -1

// assignSpeakers gives every word the voice whose turn overlaps it most.
// Words no turn covers (the recognition skips very short sounds) keep the
// voice of the word before them — or, at the very start, of the first word
// that has one — so they never start a subtitle of their own. A single word
// inside the flowing speech of another voice is folded into it: one misjudged
// word must not cut a sentence into three subtitles.
func assignSpeakers(words []wordTS, turns []speakerTurn) {
	if len(turns) == 0 {
		return
	}
	for i := range words {
		best, bestOverlap := noSpeaker, 0.0
		for _, t := range turns {
			if t.Start >= words[i].end {
				break // turns are sorted by start
			}
			ov := minF(t.End, words[i].end) - maxF(t.Start, words[i].start)
			if ov > bestOverlap {
				best, bestOverlap = t.Speaker, ov
			}
		}
		words[i].speaker = best
	}
	last := noSpeaker
	for i := range words {
		if words[i].speaker == noSpeaker {
			words[i].speaker = last
		} else {
			last = words[i].speaker
		}
	}
	// Leading words without a voice take the first one that is known.
	for i := range words {
		if words[i].speaker != noSpeaker {
			for j := 0; j < i; j++ {
				words[j].speaker = words[i].speaker
			}
			break
		}
	}
	for i := 1; i+1 < len(words); i++ {
		prev, next := words[i-1].speaker, words[i+1].speaker
		inFlow := words[i].start-words[i-1].end <= flowGap && words[i+1].start-words[i].end <= flowGap
		if words[i].speaker != prev && prev == next && inFlow {
			words[i].speaker = prev
		}
	}
}

// flowGap: a word this close to both neighbours is part of their sentence. A
// real reply ("Yes.") comes after a pause — turn-taking leaves about 0.2 s.
const flowGap = 0.1

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// shiftTurns moves turns from the WAV's timeline to the film's (the lead-in
// removed), the same shift collectWords applies to the words, sorted by start
// as assignSpeakers expects.
func shiftTurns(turns []speakerTurn, leadinSec float64) []speakerTurn {
	out := make([]speakerTurn, len(turns))
	for i, t := range turns {
		out[i] = speakerTurn{Start: t.Start - leadinSec, End: t.End - leadinSec, Speaker: t.Speaker}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// markSpeakers runs the speaker recognition for one track and reports it. It
// returns nil when it failed (the subtitle is then written without marks).
// The console gets plain lines, no bar: this runs while the next file's
// transcription bar is on screen.
func markSpeakers(ctx context.Context, cfg *AppConfig, job *fileJob, i int, t *trackJob) []speakerTurn {
	tlabel := trackLabelFor(job, i)
	emitStage(job.index+1, i+1, "speakers")
	pInfo.Printf("%s — telling speakers apart...\n", tlabel)
	started := time.Now()
	lastEmit := -1.0
	onLine := func(line string) {
		f, ok := parseProgress(line)
		if !ok {
			return
		}
		if f-lastEmit >= 0.01 || f >= 1 {
			lastEmit = f
			emitEvent(eventProgress{
				Ev: "progress", Index: job.index + 1, Track: i + 1, Tracks: len(job.tracks),
				Pct: f, ETASec: etaSeconds(started, f), Stage: "speakers",
			})
		}
	}
	out := filepath.Join(filepath.Dir(t.mainWav), "speakers.json")
	turns, err := diarize(ctx, cfg, t.mainWav, out, onLine)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		pWarn.Printf("%s — speaker recognition failed, the subtitle gets no speaker marks: %s\n", tlabel, shortErr(err))
		emitEvent(eventSpeakers{Ev: "speakers", Index: job.index + 1, Track: i + 1, Message: shortErr(err)})
		return nil
	}
	n := countSpeakers(turns)
	pInfo.Printf("%s — %d speaker(s) told apart (%s).\n", tlabel, n, fmtDuration(time.Since(started)))
	emitEvent(eventSpeakers{Ev: "speakers", Index: job.index + 1, Track: i + 1, Count: n})
	return turns
}
