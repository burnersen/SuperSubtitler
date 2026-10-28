// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestKindOfAndOwnOutput(t *testing.T) {
	cases := map[string]Kind{"a.MKV": KindVideo, "b.mp3": KindAudio, "c.de.srt": KindSubtitle, "d.txt": KindOther}
	for in, want := range cases {
		if got := KindOf(in); got != want {
			t.Errorf("KindOf(%q) = %q", in, got)
		}
	}
	for _, p := range []string{"M.subbed.mkv", "M.joined.mkv", "M.JOINED.3.mkv"} {
		if !IsOwnOutput(p) {
			t.Errorf("%q should be own output", p)
		}
	}
	for _, p := range []string{"M.mkv", "M.joined.x.mkv", "M.joined.mp4"} {
		if IsOwnOutput(p) {
			t.Errorf("%q is not own output", p)
		}
	}
}

func TestExpandPaths(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Season 1")
	_ = os.MkdirAll(sub, 0o755)
	for _, n := range []string{"b.mkv", "A.mp4", "notes.txt", "A.subbed.mkv"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(sub, "e01.mkv"), []byte("x"), 0o644)
	single := filepath.Join(dir, "b.mkv")
	txt := filepath.Join(dir, "notes.txt")

	items, rejected := ExpandPaths([]string{dir, single, txt, filepath.Join(dir, "missing.mkv")}, AcceptSubtitleInput)
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	if !reflect.DeepEqual(names, []string{"A.mp4", "b.mkv", "e01.mkv"}) {
		t.Errorf("names = %q (sorted, deduplicated, own output and txt left out)", names)
	}
	if len(rejected) != 2 {
		t.Errorf("rejected = %+v (the txt dropped on its own and the missing file)", rejected)
	}
}

func TestSubtitleArgs(t *testing.T) {
	args, err := SubtitleArgs(RunOptions{Language: " DE ", Translate: true, Speakers: true, SrtOnly: true, Auto: true, Force: true, OutDir: `D:\Subs`})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-json", "-nowait", "-nomerge", "-lang:de", "-translate", "-speakers", "-srtonly", "-auto", "-force", `-out:D:\Subs`}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %q", args)
	}
	if _, err := SubtitleArgs(RunOptions{Language: "german"}); err == nil {
		t.Error("a language name must be refused, only codes are passed on")
	}
	plain, _ := SubtitleArgs(RunOptions{})
	if !reflect.DeepEqual(plain, []string{"-json", "-nowait", "-nomerge"}) {
		t.Errorf("default args = %q", plain)
	}
}

func TestChannelAndSplitArgs(t *testing.T) {
	args, err := SubtitleArgs(RunOptions{Channel: " 2 "})
	if err != nil || args[len(args)-1] != "-channel:2" {
		t.Errorf("channel 2: %q, %v", args, err)
	}
	args, _ = SubtitleArgs(RunOptions{Channel: "0"})
	if args[len(args)-1] != "-channel:0" {
		t.Errorf("channel 0 must be passed on, not treated as automatic: %q", args)
	}
	for _, bad := range []string{"-1", "x", "16"} {
		if _, err := SubtitleArgs(RunOptions{Channel: bad}); err == nil {
			t.Errorf("channel %q must be refused", bad)
		}
	}
	if got, _ := SplitArgs(0); !reflect.DeepEqual(got, []string{"-json", "-nowait", "-split"}) {
		t.Errorf("split = %q", got)
	}
	if got, _ := SplitArgs(2); !reflect.DeepEqual(got, []string{"-json", "-nowait", "-split", "-track:2"}) {
		t.Errorf("split track 2 = %q", got)
	}
	if _, err := SplitArgs(-1); err == nil {
		t.Error("negative track must be refused")
	}
}

func TestMergeAndSetupArgs(t *testing.T) {
	if _, err := MergeArgs("", "", []string{"a.m4a"}); err == nil {
		t.Error("merge without a video must be refused")
	}
	if _, err := MergeArgs("", "v.mkv", nil); err == nil {
		t.Error("merge without extras must be refused")
	}
	args, _ := MergeArgs("out", "v.mkv", []string{"a.m4a", "s.srt"})
	if !reflect.DeepEqual(args, []string{"-json", "-nowait", "-auto", "-out:out", "v.mkv", "a.m4a", "s.srt"}) {
		t.Errorf("merge args = %q", args)
	}
	if got := SetupArgs(true); !reflect.DeepEqual(got, []string{"-json", "-nowait", "-setup", "-translate"}) {
		t.Errorf("setup args = %q", got)
	}
}

func TestBatches(t *testing.T) {
	files := []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}
	if got := Batches([]string{"-json"}, files, MaxCommandLine); len(got) != 1 {
		t.Errorf("small list should be one batch, got %d", len(got))
	}
	got := Batches([]string{"-json"}, files, 80)
	if len(got) != 3 {
		t.Fatalf("want 3 batches, got %d", len(got))
	}
	var all []string
	for _, b := range got {
		all = append(all, b...)
	}
	if !reflect.DeepEqual(all, files) {
		t.Error("batching lost or reordered files")
	}
	// A single file longer than the limit still gets its own batch.
	if got := Batches(nil, []string{strings.Repeat("x", 500)}, 100); len(got) != 1 {
		t.Error("oversized file dropped")
	}
}

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"0.2.0\r\n": "0.2.0",
		"\x1b[96m╭──╮\n│ SuperSubtitler v0.1.0 │\nUsage…": "0.1.0",
	}
	for in, want := range cases {
		if got, ok := ParseVersion(in); !ok || got != want {
			t.Errorf("ParseVersion(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := ParseVersion("garbage"); ok {
		t.Error("garbage parsed")
	}
	if !AtLeast("0.10.0", "0.2.0") || AtLeast("0.1.9", "0.2.0") || !AtLeast("v0.2.0", MinCLIVersion) {
		t.Error("AtLeast compares wrongly")
	}
}

func TestReleaseAssetURL(t *testing.T) {
	r := Release{Tag: "v0.2.0"}
	r.Assets = append(r.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{"SuperSubtitler.exe", "https://x/y.exe"})
	if u, ok := r.AssetURL("supersubtitler.EXE"); !ok || u != "https://x/y.exe" {
		t.Errorf("AssetURL = %q, %v", u, ok)
	}
}

const sampleIni = `# SuperSubtitler configuration
# Delete this file to regenerate defaults.

[detection]
# Length and start offset of the audio sample.
# A longer sample is majority-voted.
detect_seconds=180
detect_start_percent=10

[engine]
device=cuda
# only for -lowmem
compute_type_lowmem=int8

[subtitles]
sub_max_lines=2
`

func TestParseIni(t *testing.T) {
	secs := ParseIni(sampleIni)
	if len(secs) != 3 || secs[0].Name != "detection" || secs[2].Name != "subtitles" {
		t.Fatalf("sections = %+v", secs)
	}
	if !strings.HasPrefix(secs[0].Help, "Length and start") || !strings.Contains(secs[0].Help, "majority-voted") {
		t.Errorf("section help = %q", secs[0].Help)
	}
	if k := secs[0].Keys[0]; k.Help != "" || k.Doc == nil || *k.Doc.Min != 30 {
		t.Errorf("first key = %+v (its comment belongs to the section, its doc is known)", k)
	}
	if k := secs[1].Keys[1]; k.Key != "compute_type_lowmem" || k.Help != "only for -lowmem" {
		t.Errorf("key help = %+v", k)
	}
	if strings.Contains(secs[0].Help, "configuration") {
		t.Error("the file preamble must not become section help")
	}
}

func TestApplyIniValuesKeepsLayout(t *testing.T) {
	crlf := strings.ReplaceAll(sampleIni, "\n", "\r\n")
	out, err := ApplyIniValues(crlf, map[string]string{"sub_max_lines": " 3 ", "device": "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(sampleIni,
		"sub_max_lines=2", "sub_max_lines=3"), "device=cuda", "device=cpu"), "\n", "\r\n")
	if out != want {
		t.Errorf("layout changed:\n%q\nwant\n%q", out, want)
	}
	if _, err := ApplyIniValues(sampleIni, map[string]string{"nope": "1"}); err == nil {
		t.Error("unknown key must be refused")
	}
	if _, err := ApplyIniValues(sampleIni, map[string]string{"device": "cpu\nx=1"}); err == nil {
		t.Error("a value with a line break must be refused")
	}
}

func TestUpdateDecision(t *testing.T) {
	cases := []struct {
		installed, tag string
		want           bool
		msg            string
	}{
		{"0.2.0", "v0.1.0", false, "older than"}, // the bug: latest release older than what is there
		{"", "v0.1.0", false, "older than"},      // nothing installed, but the release is too old anyway
		{"0.2.0", "v0.2.0", false, "already the newest"},
		{"0.3.0", "v0.2.1", false, "newer than the newest"},
		{"0.2.0", "v0.2.1", true, ""},
		{"", "v0.2.0", true, ""},
		{"0.1.0", "v0.2.0", true, ""},
		{"0.2.0", "nightly", false, "no readable version"},
	}
	for _, c := range cases {
		got, msg := UpdateDecision(c.installed, c.tag)
		if got != c.want || !strings.Contains(msg, c.msg) {
			t.Errorf("UpdateDecision(%q, %q) = %v, %q", c.installed, c.tag, got, msg)
		}
	}
}

// Every key the CLI writes into its default INI must be explained, and no
// explanation may describe a key that does not exist (Config.go is the CLI's
// template — read from the repository so the two can never drift apart).
func TestEveryCLISettingIsExplained(t *testing.T) {
	src, err := os.ReadFile("../../Config.go")
	if err != nil {
		t.Skip("CLI sources not next to the window (", err, ")")
	}
	keys := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([a-z_]+)=%`).FindAllStringSubmatch(string(src), -1) {
		keys[m[1]] = true
	}
	if len(keys) < 20 {
		t.Fatalf("found only %d keys in Config.go - did the template move?", len(keys))
	}
	for k := range keys {
		if DocFor(k) == nil {
			t.Errorf("setting %s has no explanation in keydocs.go", k)
		}
	}
	for k := range keyDocs {
		if !keys[k] {
			t.Errorf("keydocs.go explains %s, which the CLI does not have", k)
		}
	}
}

func TestKeyDocsAreConsistent(t *testing.T) {
	for k, d := range keyDocs {
		if d.Title == "" || d.Help == "" || d.Advice == "" {
			t.Errorf("%s: title, help and advice are all needed", k)
		}
		switch d.Kind {
		case "int", "float":
			if d.Min == nil || d.Max == nil || d.RecMin == nil || d.RecMax == nil {
				t.Errorf("%s: numbers need hard limits and a recommended range", k)
				continue
			}
			if *d.RecMin < *d.Min || *d.RecMax > *d.Max || *d.RecMin > *d.RecMax {
				t.Errorf("%s: recommended %v..%v not inside %v..%v", k, *d.RecMin, *d.RecMax, *d.Min, *d.Max)
			}
			if d.Kind == "int" && (math.Trunc(*d.Min) != *d.Min || math.Trunc(*d.Max) != *d.Max) {
				t.Errorf("%s: integer limits must be whole numbers", k)
			}
		case "choice":
			if len(d.Choices) == 0 {
				t.Errorf("%s: a choice needs choices", k)
			}
		case "bool", "text":
		default:
			t.Errorf("%s: unknown kind %q", k, d.Kind)
		}
	}
}

// The recommended values must include what the CLI ships with — otherwise a
// fresh install would greet the user with warnings.
func TestDefaultsAreRecommended(t *testing.T) {
	defaults := map[string]string{
		"detect_seconds": "180", "detect_start_percent": "10", "detect_model": "large-v3-turbo",
		"model_transcribe": "large-v3-turbo", "model_translate": "large-v3", "device": "cuda",
		"compute_type": "float16", "compute_type_lowmem": "int8", "beam_size": "5",
		"vad_threshold": "0.50", "vad_speech_pad_ms": "400", "leadin_ms": "200",
		"surround_mode": "center_emphasis", "sub_max_chars_per_line": "42", "sub_max_lines": "2",
		"sub_max_cps": "17.0", "sub_min_dur_ms": "833", "sub_max_dur_ms": "7000",
		"sub_min_gap_ms": "83", "sub_gap_split_ms": "500",
	}
	for k, v := range defaults {
		d := DocFor(k)
		switch d.Kind {
		case "choice":
			found := false
			for _, c := range d.Choices {
				found = found || c.Value == v
			}
			if !found {
				t.Errorf("%s: default %s is not among the choices", k, v)
			}
		case "int", "float":
			var n float64
			if _, err := fmtSscan(v, &n); err != nil || n < *d.RecMin || n > *d.RecMax {
				t.Errorf("%s: default %s outside recommended %v..%v", k, v, *d.RecMin, *d.RecMax)
			}
		}
	}
}

func TestIniValuesAndDefaults(t *testing.T) {
	defs := IniValues(sampleIni)
	if defs["detect_seconds"] != "180" || defs["device"] != "cuda" {
		t.Errorf("IniValues = %v", defs)
	}
	secs := WithDefaults(ParseIni("[detection]\ndetect_seconds=240\n"), defs)
	if secs[0].Keys[0].Default != "180" || secs[0].Keys[0].Value != "240" {
		t.Errorf("WithDefaults = %+v", secs[0].Keys[0])
	}
}

func fmtSscan(s string, n *float64) (int, error) { return fmt.Sscan(s, n) }

func TestParseSumsAndGUIUpdate(t *testing.T) {
	sums := ParseSums("ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789  SuperSubtitlerGUI.exe\r\n" +
		"0000000000000000000000000000000000000000000000000000000000000000 *SuperSubtitler.exe\nnonsense\n")
	if sums["supersubtitlergui.exe"] != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" || len(sums) != 2 {
		t.Errorf("ParseSums = %v", sums)
	}
	if !GUIUpdate("0.3.4", "v0.3.5") || GUIUpdate("0.3.5", "v0.3.5") || GUIUpdate("0.4.0", "v0.3.5") || GUIUpdate("0.3.4", "latest") {
		t.Error("GUIUpdate must only go forward")
	}
}
