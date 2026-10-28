//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// captureEvents switches the emitters to a temp file for the duration of fn and
// returns every event line, decoded.
func captureEvents(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "events-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	oldMode, oldSink := jsonMode, jsonSink
	jsonMode, jsonSink = true, f
	defer func() { jsonMode, jsonSink = oldMode, oldSink }()

	fn()

	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not one JSON object per line: %q (%v)", sc.Text(), err)
		}
		out = append(out, m)
	}
	f.Close()
	return out
}

// withStdin feeds the shared answer reader for the duration of fn.
func withStdin(input string, fn func()) {
	old := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(input))
	defer func() { stdinReader = old }()
	fn()
}

func testStreams() []probeStream {
	return []probeStream{
		{CodecType: "audio", CodecName: "ac3", Channels: 6, ChannelLayout: "5.1(side)", Tags: map[string]string{"language": "ger"}},
		{CodecType: "audio", CodecName: "aac", Channels: 2, Tags: map[string]string{"language": "eng", "title": "Commentary"}},
	}
}

func testCands() []trackCandidate {
	var out []trackCandidate
	for i, s := range testStreams() {
		out = append(out, trackCandidate{ordinal: i, stream: s, lang: containerLang(s)})
	}
	return out
}

func TestTrackQuestionEventAndAnswer(t *testing.T) {
	var got []int
	events := captureEvents(t, func() {
		withStdin("2\n", func() { got = promptTrackSelection(testCands(), "M.mkv", false) })
	})
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("answer \"2\" chose %v", got)
	}
	if len(events) != 1 || events[0]["ev"] != "question" || events[0]["kind"] != questionTracks {
		t.Fatalf("events = %v", events)
	}
	opts := events[0]["options"].([]any)
	second := opts[1].(map[string]any)
	if len(opts) != 2 || second["n"].(float64) != 2 || second["lang"] != "en" || !strings.Contains(second["label"].(string), "Commentary") {
		t.Errorf("options = %v", opts)
	}
}

func TestQuestionsFallBackWhenStdinCloses(t *testing.T) {
	captureEvents(t, func() {
		withStdin("", func() {
			if got := promptTrackSelection(testCands(), "M.mkv", false); len(got) != 2 {
				t.Errorf("closed stdin should take the suggested tracks (here one per language: both), got %v", got)
			}
			if got := resolveLangDiscrepancy("de", "en", "M.mkv"); got != "en" {
				t.Errorf("closed stdin should keep the detected language, got %q", got)
			}
			// Used to loop forever on a closed stdin.
			if got := promptForLanguage("M.mkv"); got != "" {
				t.Errorf("closed stdin should let the engine guess, got %q", got)
			}
			if !askMerge("v.mkv", []string{"a.m4a"}, nil, false) {
				t.Error("closed stdin should merge (default yes)")
			}
		})
	})
}

func TestLanguageMismatchQuestion(t *testing.T) {
	var got string
	events := captureEvents(t, func() {
		withStdin("ger\n", func() { got = resolveLangDiscrepancy("de", "en", "M.mkv") })
	})
	if got != "de" {
		t.Errorf("answer ger should give de, got %q", got)
	}
	q := events[0]
	if q["kind"] != questionLangMismatch || q["tag"] != "de" || q["detected"] != "en" || q["default"] != "en" {
		t.Errorf("question = %v", q)
	}
}

func TestAutoNeverAsks(t *testing.T) {
	events := captureEvents(t, func() {
		withStdin("1\n", func() {
			if got := promptTrackSelection(testCands(), "M.mkv", true); len(got) != 2 {
				t.Errorf("-auto should take the suggested tracks (one per language: both), got %v", got)
			}
			if !askMerge("v.mkv", nil, []string{"s.srt"}, true) {
				t.Error("-auto should merge")
			}
		})
		if line, _ := readAnswerLine(); line != "" {
			// stdin is restored after withStdin; nothing to check here
		}
	})
	for _, e := range events {
		if e["ev"] == "question" {
			t.Fatalf("-auto emitted a question: %v", e)
		}
	}
}

func TestResultAndFileEvents(t *testing.T) {
	job := &fileJob{index: 0, src: `C:\v\Movie.mkv`, duration: 12.5, status: "ok", outPath: `C:\v\Movie.subbed.mkv`}
	job.tracks = []*trackJob{{stream: testStreams()[0], ordinal: 0, srtPath: `C:\v\Movie.de.srt`}}
	bad := &fileJob{index: 1, src: `C:\v\Bad.mkv`}
	bad.fail(os.ErrNotExist)

	events := captureEvents(t, func() {
		emitFile(job, 2)
		emitResult(job)
		emitResult(bad)
	})
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	f := events[0]
	if f["ev"] != "file" || f["index"].(float64) != 1 || f["total"].(float64) != 2 {
		t.Errorf("file event = %v", f)
	}
	if tr := f["tracks"].([]any)[0].(map[string]any); tr["n"].(float64) != 1 || tr["lang"] != "de" {
		t.Errorf("track = %v", tr)
	}
	ok := events[1]
	outs := ok["outputs"].([]any)
	if ok["status"] != "success" || len(outs) != 2 || outs[0] != `C:\v\Movie.de.srt` || outs[1] != `C:\v\Movie.subbed.mkv` {
		t.Errorf("success result = %v", ok)
	}
	if fail := events[2]; fail["status"] != "failed" || fail["index"].(float64) != 2 || fail["message"] == "" {
		t.Errorf("failed result = %v", fail)
	}
}

func TestEmittersAreSilentWithoutJSON(t *testing.T) {
	old := jsonMode
	jsonMode = false
	defer func() { jsonMode = old }()
	// Must not panic with a nil sink.
	emitError("x")
	emitStage(1, 1, "detect")
	emitResult(&fileJob{src: "a"})
}
