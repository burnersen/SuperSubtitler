//go:build windows && amd64

package main

import (
	"strings"
	"testing"
)

func TestSRTTime(t *testing.T) {
	cases := map[float64]string{
		0:       "00:00:00,000",
		-3:      "00:00:00,000",
		1.5:     "00:00:01,500",
		61.25:   "00:01:01,250",
		3725.04: "01:02:05,040",
	}
	for in, want := range cases {
		if got := srtTime(in); got != want {
			t.Errorf("srtTime(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestWrapTextKeepsShortLineAndBalancesTwoLines(t *testing.T) {
	if got := wrapText("Hello there", 42, 2); len(got) != 1 || got[0] != "Hello there" {
		t.Fatalf("short text should stay on one line, got %q", got)
	}
	text := "This sentence is clearly too long to fit on one single line"
	got := wrapText(text, 42, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %q", got)
	}
	for _, l := range got {
		if len(l) > 42 {
			t.Errorf("line longer than 42: %q", l)
		}
	}
	if strings.Join(got, " ") != text {
		t.Errorf("words lost or reordered: %q", got)
	}
}

func TestWrapTextNeverDropsWords(t *testing.T) {
	text := strings.Repeat("word ", 40)
	got := wrapText(text, 20, 2)
	if len(got) > 2 {
		t.Fatalf("more than maxLines: %d", len(got))
	}
	if strings.Join(got, " ") != strings.TrimSpace(text) {
		t.Errorf("words lost while folding overflow")
	}
}

func TestBuildSRTRemovesLeadinAndSplitsOnPause(t *testing.T) {
	s := defaultSettings()
	wj := &whisperJSON{Segments: []whisperSegment{
		{Words: []whisperWord{{Start: 0.2, End: 0.6, Word: " Hello"}, {Start: 0.6, End: 1.0, Word: " world"}}},
		// A pause far longer than sub_gap_split_ms starts a new cue.
		{Words: []whisperWord{{Start: 5.2, End: 5.8, Word: " Again"}}},
	}}
	srt := buildSRT(wj, 0.2, s)
	if !strings.HasPrefix(srt, "1\n00:00:00,000 --> ") {
		t.Fatalf("lead-in not removed from first cue:\n%s", srt)
	}
	if !strings.Contains(srt, "Hello world") || !strings.Contains(srt, "\n2\n00:00:05,000 --> ") {
		t.Fatalf("unexpected cues:\n%s", srt)
	}
}

func TestEnforceTimingMinDurationAndGap(t *testing.T) {
	s := defaultSettings()
	cues := []cue{
		{start: 1.0, end: 1.1, lines: []string{"Hi"}},
		{start: 1.5, end: 2.5, lines: []string{"Next"}},
	}
	enforceTiming(cues, s)
	minGap := float64(s.subMinGapMs) / 1000
	if cues[0].end > cues[1].start-minGap+1e-9 {
		t.Errorf("cue 1 overlaps the anti-flicker gap: end %.3f, next %.3f", cues[0].end, cues[1].start)
	}
	if cues[1].end-cues[1].start < float64(s.subMinDurMs)/1000-1e-9 {
		t.Errorf("minimum duration not applied: %.3f", cues[1].end-cues[1].start)
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText("  a​ b \tc  "); got != "a b c" && got != "a bc" {
		t.Errorf("cleanText = %q", got)
	}
	if got := cleanText("\ufeff"); got != "" {
		t.Errorf("BOM only should be empty, got %q", got)
	}
}
