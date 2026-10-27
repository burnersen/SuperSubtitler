package core

import (
	"os"
	"path/filepath"
	"reflect"
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
	args, err := SubtitleArgs(RunOptions{Language: " DE ", Translate: true, SrtOnly: true, Auto: true, Force: true, OutDir: `D:\Subs`})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-json", "-nowait", "-nomerge", "-lang:de", "-translate", "-srtonly", "-auto", "-force", `-out:D:\Subs`}
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
	if secs[0].Keys[0].Help != "" || secs[0].Keys[0].Hint != "30 – 1800" {
		t.Errorf("first key = %+v (its comment belongs to the section)", secs[0].Keys[0])
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
