//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// JSONEvents.go — machine-readable run reporting for front-ends (-json).
//
// With -json the COMPLETE human display moves to stderr and stdout carries
// exactly one JSON object per line and event. A front-end (SuperSubtitlerGUI)
// follows the run from these events instead of picking screen text apart, so a
// cosmetic change to the console output can never break it.
//
// Without -json every emit function returns immediately and nothing is
// redirected — the console experience is untouched.
//
// Questions: whenever the tool would ask on the console it first emits a
// "question" event, then reads ONE line from stdin — exactly the line a person
// would type (see the kinds below). In -json mode it waits for that line
// without a time limit; if stdin is closed instead, the documented default
// answer applies. The front-end must send exactly one line per question and
// only after the question event (answers are not queued ahead).
//
// Event overview (field "ev"):
//
//	run       once, first: version, mode (subtitle|merge|split|setup), files
//	setup     first-run installation steps with download progress
//	skip      an input that is not processed (already subtitled, own output)
//	file      a queued file (index is 1-based and stays the file's id)
//	stage     what a file/track is doing now (probe|detect|extract|transcribe|mux)
//	lang      the resolved language plan of a track
//	progress  transcription progress of a track (0..1, with ETA)
//	question  the tool waits for one stdin line
//	result    a file is finished (success|failed)
//	error     a fatal error that ends the run (setup failed, no tools, …)
//	summary   always last: totals, elapsed time, cancelled flag

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pterm/pterm"
)

// jsonMode is set once at start (-json) and only read afterwards.
var jsonMode bool

// jsonSink is the REAL stdout, saved before os.Stdout is redirected. Events go
// here and nowhere else.
var jsonSink *os.File

// jsonMu serialises writes: the extract and finalize stages run on their own
// goroutines, and two interleaved half lines would be unreadable.
var jsonMu sync.Mutex

// stdinReader is the ONE buffered reader over stdin. Every question reads
// through it; a fresh bufio.Reader per question could swallow a following
// answer into its private buffer when stdin is a pipe.
var stdinReader = bufio.NewReader(os.Stdin)

// readAnswerLine reads one answer line. ok is false when stdin is closed (no
// console, front-end gone) — callers then fall back to their default.
func readAnswerLine() (string, bool) {
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// enableJSONMode separates the two channels. Must run before the first output.
// The three redirections are all needed: os.Stdout catches fmt.*, the default
// output catches pterm printers/boxes/tables, and the animated spinner and bar
// own their writers (silenced: on a pipe every redraw would become a new line).
func enableJSONMode() {
	jsonMode = true
	jsonSink = os.Stdout
	os.Stdout = os.Stderr
	pterm.SetDefaultOutput(os.Stderr)
	pterm.DefaultSpinner = *pterm.DefaultSpinner.WithWriter(io.Discard)
	pterm.DefaultProgressbar = *pterm.DefaultProgressbar.WithWriter(io.Discard)
}

// emitEvent writes one event as one JSON line. Write errors are swallowed on
// purpose: the only realistic cause is a front-end that went away, and the
// running work should still finish cleanly.
func emitEvent(v any) {
	if !jsonMode || jsonSink == nil {
		return
	}
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	line = append(line, '\n')
	jsonMu.Lock()
	defer jsonMu.Unlock()
	_, _ = jsonSink.Write(line)
}

// ----------------------------------------------------------------------------
// Event shapes (one struct per kind, so every field belongs somewhere obvious)
// ----------------------------------------------------------------------------

type eventRun struct {
	Ev        string `json:"ev"` // "run"
	Version   string `json:"version"`
	Mode      string `json:"mode"` // subtitle | merge | split | setup
	Files     int    `json:"files"`
	Translate bool   `json:"translate,omitempty"`
	Auto      bool   `json:"auto,omitempty"`
}

type eventSetup struct {
	Ev      string  `json:"ev"`    // "setup"
	Step    string  `json:"step"`  // ffmpeg | python | pip | engine | cuda | model | ready
	State   string  `json:"state"` // start | progress | done
	Msg     string  `json:"msg,omitempty"`
	DoneMB  float64 `json:"done_mb,omitempty"`
	TotalMB float64 `json:"total_mb,omitempty"` // 0 = size unknown
	MBps    float64 `json:"mbps,omitempty"`
}

type eventSkip struct {
	Ev     string `json:"ev"` // "skip"
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"` // has_subs | own_output
}

type eventFile struct {
	Ev       string       `json:"ev"` // "file"
	Index    int          `json:"index"`
	Total    int          `json:"total"`
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	Duration float64      `json:"duration_sec,omitempty"`
	Audio    bool         `json:"audio_only,omitempty"`
	Tracks   []eventTrack `json:"tracks,omitempty"`
	Error    string       `json:"error,omitempty"`
}

// eventTrack is one selected audio stream of a file. N is the 1-based number
// among the file's audio streams — the same number -track:N takes.
type eventTrack struct {
	N        int    `json:"n"`
	Lang     string `json:"lang,omitempty"`
	Codec    string `json:"codec,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Title    string `json:"title,omitempty"`
}

type eventStage struct {
	Ev    string `json:"ev"` // "stage"
	Index int    `json:"index"`
	Track int    `json:"track,omitempty"` // 1-based position in the file's selection
	Stage string `json:"stage"`           // probe | detect | extract | transcribe | mux
}

type eventLang struct {
	Ev       string `json:"ev"` // "lang"
	Index    int    `json:"index"`
	Track    int    `json:"track,omitempty"`
	Detected string `json:"detected,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Src      string `json:"src,omitempty"`
	Out      string `json:"out"`
	Task     string `json:"task"`
	Model    string `json:"model"`
}

type eventProgress struct {
	Ev     string  `json:"ev"` // "progress"
	Index  int     `json:"index"`
	Track  int     `json:"track,omitempty"`
	Tracks int     `json:"tracks,omitempty"`
	Pct    float64 `json:"pct"` // 0..1 of this track
	ETASec float64 `json:"eta_sec,omitempty"`
}

type eventResult struct {
	Ev      string   `json:"ev"` // "result"
	Index   int      `json:"index"`
	Status  string   `json:"status"` // success | failed
	Name    string   `json:"name"`
	Outputs []string `json:"outputs,omitempty"`
	Message string   `json:"message,omitempty"`
}

type eventError struct {
	Ev      string `json:"ev"` // "error"
	Message string `json:"message"`
}

type eventSummary struct {
	Ev         string  `json:"ev"` // "summary"
	Mode       string  `json:"mode"`
	Files      int     `json:"files"`
	Success    int     `json:"success"`
	Skipped    int     `json:"skipped"`
	Failed     int     `json:"failed"`
	ElapsedSec float64 `json:"elapsed_sec"`
	Cancelled  bool    `json:"cancelled,omitempty"`
}

// Question kinds and the answer line each expects (same as a person types):
//
//	tracks          "1,3" = those audio tracks; empty = all
//	lang_mismatch   a language code; empty = the detected language
//	lang_unknown    a language code; empty = let the engine guess ("und")
//	merge           "y" or "n"; empty = yes
const (
	questionTracks       = "tracks"
	questionLangMismatch = "lang_mismatch"
	questionLangUnknown  = "lang_unknown"
	questionMerge        = "merge"
)

type eventQuestion struct {
	Ev       string           `json:"ev"` // "question"
	Kind     string           `json:"kind"`
	File     string           `json:"file,omitempty"`
	Hint     string           `json:"hint,omitempty"`
	Options  []questionOption `json:"options,omitempty"`
	Tag      string           `json:"tag,omitempty"`      // lang_mismatch: container tag
	Detected string           `json:"detected,omitempty"` // lang_mismatch: detection result
	Default  string           `json:"default,omitempty"`  // what an empty line means
}

// questionOption is one entry of a choice list; N is the number to answer with.
type questionOption struct {
	N     int    `json:"n"`
	Label string `json:"label"`
	Lang  string `json:"lang,omitempty"`
}

// ----------------------------------------------------------------------------
// Convenience senders
// ----------------------------------------------------------------------------

func emitRun(mode string, files int, cfg *AppConfig) {
	ev := eventRun{Ev: "run", Version: appVersion, Mode: mode, Files: files}
	if cfg != nil {
		ev.Translate = cfg.translate
		ev.Auto = cfg.auto
	}
	emitEvent(ev)
}

func emitSetup(step, state, msg string, doneMB, totalMB, mbps float64) {
	emitEvent(eventSetup{Ev: "setup", Step: step, State: state, Msg: msg, DoneMB: doneMB, TotalMB: totalMB, MBps: mbps})
}

func emitSkip(path, reason string) {
	emitEvent(eventSkip{Ev: "skip", Name: filepath.Base(path), Path: path, Reason: reason})
}

func emitFile(job *fileJob, total int) {
	if !jsonMode {
		return
	}
	ev := eventFile{
		Ev: "file", Index: job.index + 1, Total: total,
		Name: filepath.Base(job.src), Path: job.src,
		Duration: job.duration, Audio: isAudioFile(job.src),
	}
	for _, t := range job.tracks {
		ev.Tracks = append(ev.Tracks, eventTrack{
			N: t.ordinal + 1, Lang: containerLang(t.stream), Codec: t.stream.CodecName,
			Channels: t.stream.Channels, Title: t.stream.Tags["title"],
		})
	}
	if job.err != nil {
		ev.Error = shortErr(job.err)
	}
	emitEvent(ev)
}

func emitStage(index, track int, stage string) {
	emitEvent(eventStage{Ev: "stage", Index: index, Track: track, Stage: stage})
}

func emitResult(job *fileJob) {
	if !jsonMode {
		return
	}
	ev := eventResult{Ev: "result", Index: job.index + 1, Name: filepath.Base(job.src)}
	if job.status == "ok" {
		ev.Status = "success"
		for _, t := range job.tracks {
			if t.srtPath != "" {
				ev.Outputs = append(ev.Outputs, t.srtPath)
			}
		}
		if job.outPath != "" {
			ev.Outputs = append(ev.Outputs, job.outPath)
		}
	} else {
		ev.Status = "failed"
		if job.err != nil {
			ev.Message = shortErr(job.err)
		}
	}
	emitEvent(ev)
}

func emitError(msg string) {
	emitEvent(eventError{Ev: "error", Message: msg})
}

func emitSummary(mode string, files, success, skipped, failed int, started time.Time, cancelled bool) {
	emitEvent(eventSummary{
		Ev: "summary", Mode: mode, Files: files,
		Success: success, Skipped: skipped, Failed: failed,
		ElapsedSec: time.Since(started).Seconds(), Cancelled: cancelled,
	})
}

func emitQuestion(q eventQuestion) {
	q.Ev = "question"
	emitEvent(q)
}
