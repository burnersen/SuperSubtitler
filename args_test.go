//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cfg := parseArgs([]string{
		"-translate", "-LANG:de", "-track:2", "-channel:0", "-srtonly", "-lean",
		"-novad", "-lowmem", "-auto", "-force", "-nowait", "-json", "-setup",
		`-out:"D:\Subs"`, "-bogus", "movie.mkv", `C:\Folder`,
	})
	if !cfg.translate || cfg.forcedLang != "de" || cfg.forcedTrack != 2 || cfg.forcedChannel != 0 {
		t.Errorf("language/track flags wrong: %+v", cfg)
	}
	if !cfg.srtOnly || !cfg.lean || !cfg.noVad || !cfg.lowMem || !cfg.auto || !cfg.force ||
		!cfg.noWait || !cfg.json || !cfg.setup {
		t.Errorf("switches wrong: %+v", cfg)
	}
	if cfg.outDir != `D:\Subs` {
		t.Errorf("outDir = %q (quotes must be stripped)", cfg.outDir)
	}
	if !reflect.DeepEqual(cfg.unknownFlags, []string{"-bogus"}) {
		t.Errorf("unknownFlags = %q", cfg.unknownFlags)
	}
	if !reflect.DeepEqual(cfg.inputArgs, []string{"movie.mkv", `C:\Folder`}) {
		t.Errorf("inputArgs = %q", cfg.inputArgs)
	}
	if d := parseArgs(nil); d.forcedChannel != -1 || d.auto || d.json {
		t.Errorf("defaults wrong: %+v", d)
	}
}

func TestParseTrackChoice(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []int
	}{
		{"", 3, []int{0, 1, 2}},
		{"1,3", 3, []int{0, 2}},
		{"3 1 3", 3, []int{2, 0}},
		{"9, x, 0", 3, []int{0, 1, 2}},
		{"2;", 2, []int{1}},
	}
	for _, c := range cases {
		if got := parseTrackChoice(c.in, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseTrackChoice(%q,%d) = %v, want %v", c.in, c.n, got, c.want)
		}
	}
}

func TestIsOwnOutput(t *testing.T) {
	yes := []string{`C:\a\Movie.subbed.mkv`, `Movie.JOINED.mkv`, `Movie.joined.2.mkv`}
	no := []string{`Movie.mkv`, `Movie.joined.x.mkv`, `joined.mp4`, `Movie.joinedcut.mkv`}
	for _, p := range yes {
		if !isOwnOutput(p) {
			t.Errorf("isOwnOutput(%q) = false", p)
		}
	}
	for _, p := range no {
		if isOwnOutput(p) {
			t.Errorf("isOwnOutput(%q) = true", p)
		}
	}
}

func TestOutputBase(t *testing.T) {
	src := filepath.Join("films", "Movie.mkv")
	if got := outputBase(&AppConfig{}, src); got != filepath.Join("films", "Movie") {
		t.Errorf("default outputBase = %q", got)
	}
	if got := outputBase(&AppConfig{outDir: "out"}, src); got != filepath.Join("out", "Movie") {
		t.Errorf("-out outputBase = %q", got)
	}
}

// A bracket in a film name used to be read as a glob character class, so an
// existing "Movie [1080p].de.srt" was never found and the file was redone.
func TestHasExistingSubsWithBrackets(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "Movie [1080p]")
	if hasExistingSubs(base) {
		t.Fatal("nothing written yet")
	}
	if err := os.WriteFile(base+".de.srt", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !hasExistingSubs(base) {
		t.Error("existing .de.srt next to a bracketed name not found")
	}
}

func TestCollectInputFilesSkipsAndForce(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("a.mkv")
	write("b.mkv")
	write("b.en.srt")
	write("c.subbed.mkv") // own output of a source that is gone
	write("notes.txt")

	files, skipped := collectInputFiles(&AppConfig{}, []string{dir})
	if len(files) != 1 || filepath.Base(files[0]) != "a.mkv" {
		t.Errorf("files = %q", files)
	}
	if countReason(skipped, skipHasSubs) != 1 || countReason(skipped, skipOwnOutput) != 1 {
		t.Errorf("skipped = %+v", skipped)
	}
	files, _ = collectInputFiles(&AppConfig{force: true}, []string{dir})
	if len(files) != 2 {
		t.Errorf("-force should take b.mkv again, files = %q", files)
	}
	files, _ = collectInputFiles(&AppConfig{split: true}, []string{dir})
	if len(files) != 2 {
		t.Errorf("-split must not skip subtitled files, files = %q", files)
	}
}

func TestUniqueSRTPath(t *testing.T) {
	used := map[string]bool{}
	a := uniqueSRTPath("M", "de", used)
	b := uniqueSRTPath("M", "de", used)
	if a != "M.de.srt" || b != "M.de.2.srt" {
		t.Errorf("got %q, %q", a, b)
	}
}

func TestFileLangFromName(t *testing.T) {
	cases := map[string]string{
		"Movie.de.srt":     "de",
		"audio.eng.m4a":    "en",
		"Movie.hd.srt":     "",
		"Movie.srt":        "",
		"Movie.joined.ac3": "",
	}
	for in, want := range cases {
		if got := fileLangFromName(in); got != want {
			t.Errorf("fileLangFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseYes(t *testing.T) {
	for in, want := range map[string]bool{"": true, "y": true, "JA": true, "n": false, "nein": false, "x": false} {
		if got := parseYes(in, true); got != want {
			t.Errorf("parseYes(%q) = %v", in, got)
		}
	}
	if parseYes("", false) {
		t.Error("empty must return the default")
	}
}

func TestModelsNeeded(t *testing.T) {
	s := appSettings
	defer func() { appSettings = s }()
	appSettings.detectModel, appSettings.modelTranscribe, appSettings.modelTranslate = "turbo", "turbo", "large"

	if got := modelsNeeded(&AppConfig{}); !reflect.DeepEqual(got, []string{"turbo"}) {
		t.Errorf("default = %q (detect and transcribe share a model)", got)
	}
	if got := modelsNeeded(&AppConfig{translate: true}); !reflect.DeepEqual(got, []string{"turbo", "large"}) {
		t.Errorf("translate = %q", got)
	}
	if got := modelsNeeded(&AppConfig{forcedLang: "de", translate: true}); !reflect.DeepEqual(got, []string{"large"}) {
		t.Errorf("forced language needs no detection model, got %q", got)
	}
}

func TestExitCode(t *testing.T) {
	if exitCode(0, false) != exitOK || exitCode(2, false) != exitFailed || exitCode(0, true) != exitCancelled {
		t.Error("exit codes wrong")
	}
}

func TestProgressText(t *testing.T) {
	if got := progressText("Downloading FFmpeg", 50*bytesPerMB, 200*bytesPerMB, 5.25); got != "Downloading FFmpeg — 50.0 / 200.0 MB (25%) · 5.2 MB/s" &&
		got != "Downloading FFmpeg — 50.0 / 200.0 MB (25%) · 5.3 MB/s" {
		t.Errorf("progressText = %q", got)
	}
	if got := progressText("Model", 3*bytesPerMB/2, 0, 0); got != "Model — 1.5 MB" {
		t.Errorf("unknown total = %q", got)
	}
}

func TestPipStatus(t *testing.T) {
	if msg, ok := pipStatus("  Downloading nvidia_cudnn_cu12-9.1.whl (664.8 MB)"); !ok || msg != "Downloading nvidia_cudnn_cu12-9.1.whl (664.8 MB)" {
		t.Errorf("pipStatus = %q, %v", msg, ok)
	}
	if _, ok := pipStatus("   ---------------------------------------- 1.2/3.4 MB"); ok {
		t.Error("progress noise must not become status")
	}
}

func TestModelMarkerRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), modelMarkerName)
	in := map[string]string{"large-v3": `C:\m\large`, "large-v3-turbo": `C:\m\turbo`}
	if err := writeModelMarker(p, in); err != nil {
		t.Fatal(err)
	}
	if got := readModelMarker(p); !reflect.DeepEqual(got, in) {
		t.Errorf("round trip = %v", got)
	}
}

func TestNewToolFlags(t *testing.T) {
	cfg := parseArgs([]string{"-nomerge", `-checkconfig:"C:\x\a.ini"`})
	if !cfg.noMerge || cfg.checkConfig != `C:\x\a.ini` {
		t.Errorf("got %+v", cfg)
	}
}

func TestCheckConfigFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.ini")
	bad := filepath.Join(dir, "bad.ini")
	_ = os.WriteFile(good, []byte("[subtitles]\nsub_max_lines=2\n"), 0644)
	_ = os.WriteFile(bad, []byte("sub_max_lines=9\n"), 0644)
	if err := checkConfigFile(good); err != nil {
		t.Errorf("good config rejected: %v", err)
	}
	if err := checkConfigFile(bad); err == nil || !strings.Contains(err.Error(), "sub_max_lines") {
		t.Errorf("bad config: %v", err)
	}
	if data, _ := os.ReadFile(bad); string(data) != "sub_max_lines=9\n" {
		t.Error("-checkconfig must never rewrite the file")
	}
}

func TestDefaultConfigTextIsValidAndComplete(t *testing.T) {
	text := defaultConfigText()
	if p := settingsProblem(parseSettings(text)); p != "" {
		t.Fatalf("the default INI does not pass its own check: %s", p)
	}
	if got := parseSettings(text); got != defaultSettings() {
		t.Errorf("parsing the default INI does not give the defaults:\n%+v\n%+v", got, defaultSettings())
	}
	if !parseArgs([]string{"-printdefaults"}).printDefaults {
		t.Error("-printdefaults not parsed")
	}
}

func TestNewSettingLimits(t *testing.T) {
	for _, bad := range []string{"vad_speech_pad_ms=-5", "sub_min_gap_ms=5000", "sub_gap_split_ms=0"} {
		if settingsProblem(parseSettings(bad)) == "" {
			t.Errorf("%s must be refused", bad)
		}
	}
}

func TestSpeakerFlagsAndSettings(t *testing.T) {
	if cfg := parseArgs([]string{"-speakers"}); !cfg.speakers || cfg.speakerCount != 0 {
		t.Errorf("-speakers: %+v", cfg)
	}
	if cfg := parseArgs([]string{"-Speakers:3"}); !cfg.speakers || cfg.speakerCount != 3 {
		t.Errorf("-speakers:3: %+v", cfg)
	}
	if cfg := parseArgs([]string{"-speakers:x"}); !cfg.speakers || cfg.speakerCount != 0 {
		t.Errorf("-speakers:x must fall back to automatic: %+v", cfg)
	}
	for _, bad := range []string{"speaker_threshold=0.1", "speaker_threshold=0.95", "speaker_count=-1", "speaker_count=21"} {
		if settingsProblem(parseSettings(bad)) == "" {
			t.Errorf("%s must be refused", bad)
		}
	}
	s := appSettings
	defer func() { appSettings = s }()
	appSettings.speakerCount = 4
	if speakerCountFor(&AppConfig{}) != 4 || speakerCountFor(&AppConfig{speakerCount: 2}) != 2 {
		t.Error("-speakers:N must win over speaker_count, which applies otherwise")
	}
}

func TestModelsNeededWithSpeakers(t *testing.T) {
	s := appSettings
	defer func() { appSettings = s }()
	appSettings.detectModel, appSettings.modelTranscribe = "turbo", "turbo"
	want := []string{"turbo", speakerSegmentationModel, speakerEmbeddingModel}
	if got := modelsNeeded(&AppConfig{speakers: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("-speakers = %q", got)
	}
	if got := modelsNeeded(&AppConfig{setup: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("-setup must install the speaker models too, got %q", got)
	}
	for _, m := range want[1:] {
		if _, ok := pins.Models[m]; !ok {
			t.Errorf("%s is not pinned", m)
		}
	}
}
