//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// Speakers.go — speaker marking (-speakers).
//
// After a track is transcribed, transcribe.py --diarize finds the moments the
// speaking voice changes (pyannote segmentation 3.0 through onnxruntime, on
// the processor, a process of its own) while the graphics card already works
// on the next file. Who is who across the film is never needed: the subtitles
// only show where the voice changes. The result is a list of stretches
// between the changes, which Subtitle.go uses to start a new subtitle when
// the voice changes and to put two short lines of different speakers into
// one subtitle as "- " lines. If it fails, the subtitle is written without
// marks: a missing mark never costs the subtitle itself.
//
// How the method was chosen: the speaker benchmark (.github/workflows/
// speaker-benchmark.yml) scores on hand-labelled recordings how many real
// changes are found - short ones (a reply under 2 s) on their own - and how
// many found changes are real. Telling voices apart across the film
// (sherpa-onnx with a speaker embedding, 0.3.0-0.3.1) found 46 % of the short
// changes; this finds 58 % at the same precision, about 8 times faster.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// speakerSegmentationModel is the pinned model (runtime.json).
const speakerSegmentationModel = "speaker-segmentation"

// speakerTurn is one stretch between two voice changes, in the WAV's timeline
// (that is, including the lead-in, like the engine's word times). Speaker
// numbers only alternate (0, 1, 0 ...): neighbours differ, nothing more.
type speakerTurn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker int     `json:"speaker"`
}

// speakerResult is what transcribe.py --diarize writes.
type speakerResult struct {
	Changes []float64     `json:"changes"`
	Turns   []speakerTurn `json:"turns"`
}

// diarize finds the voice changes in wavPath. onLine receives the driver's
// "@PROGRESS x" lines.
func diarize(ctx context.Context, wavPath, outPath string, onLine func(string)) (*speakerResult, error) {
	args := []string{whisperDriverPath, "--diarize",
		"--segmentation", filepath.Join(modelArg(speakerSegmentationModel), "model.onnx"),
		"--threshold", strconv.FormatFloat(appSettings.speakerChangeThreshold, 'f', 2, 64),
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
	return parseSpeakerResult(data)
}

func parseSpeakerResult(data []byte) (*speakerResult, error) {
	var out speakerResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("Speakers.go: parseSpeakerResult: %w", err)
	}
	return &out, nil
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

// markSpeakers finds the voice changes of one track and reports them. It
// returns nil when that failed (the subtitle is then written without marks).
// The console gets plain lines, no bar: this runs while the next file's
// transcription bar is on screen.
func markSpeakers(ctx context.Context, job *fileJob, i int, t *trackJob) []speakerTurn {
	tlabel := trackLabelFor(job, i)
	emitStage(job.index+1, i+1, "speakers")
	pInfo.Printf("%s — finding speaker changes...\n", tlabel)
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
	res, err := diarize(ctx, t.mainWav, out, onLine)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		pWarn.Printf("%s — speaker changes could not be found, the subtitle gets no speaker marks: %s\n", tlabel, shortErr(err))
		emitEvent(eventSpeakers{Ev: "speakers", Index: job.index + 1, Track: i + 1, Message: shortErr(err)})
		return nil
	}
	pInfo.Printf("%s — %d speaker change(s) found (%s).\n", tlabel, len(res.Changes), fmtDuration(time.Since(started)))
	emitEvent(eventSpeakers{Ev: "speakers", Index: job.index + 1, Track: i + 1, Changes: len(res.Changes)})
	return res.Turns
}
