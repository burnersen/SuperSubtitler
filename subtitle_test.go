//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"reflect"
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
	srt := buildSRT(wj, 0.2, s, nil)
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

// dialogueWJ builds engine output; each word is "text@start-end".
func dialogueWJ(parts ...[]string) *whisperJSON {
	wj := &whisperJSON{}
	for _, p := range parts {
		var seg whisperSegment
		for _, w := range p {
			text, span, _ := strings.Cut(w, "@")
			a, b, _ := strings.Cut(span, "-")
			seg.Words = append(seg.Words, whisperWord{Start: atof(a), End: atof(b), Word: " " + text})
		}
		wj.Segments = append(wj.Segments, seg)
	}
	return wj
}

func atof(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%g", &f)
	return f
}

func TestAssignSpeakersByOverlapAndSmoothing(t *testing.T) {
	words := []wordTS{
		{0.0, 0.4, "a", noSpeaker, true},   // before any turn: takes the first voice
		{1.0, 1.4, "b", noSpeaker, true},   // speaker 0
		{1.45, 1.85, "c", noSpeaker, true}, // a misjudged turn of 1 inside flowing speech of 0
		{1.9, 2.8, "d", noSpeaker, true},
		{3.5, 3.9, "e", noSpeaker, true}, // in a gap: keeps the voice before it
		{5.0, 5.4, "f", noSpeaker, true}, // speaker 1
	}
	turns := []speakerTurn{
		{Start: 0.9, End: 1.45, Speaker: 0},
		{Start: 1.45, End: 1.9, Speaker: 1},
		{Start: 1.9, End: 3.0, Speaker: 0},
		{Start: 4.9, End: 6.0, Speaker: 1},
	}
	assignSpeakers(words, turns)
	var got []int
	for _, w := range words {
		got = append(got, w.speaker)
	}
	if want := []int{0, 0, 0, 0, 0, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("speakers = %v, want %v", got, want)
	}
}

func TestBuildSRTSpeakerChangeStartsNewCueAndShortExchangeGetsDashes(t *testing.T) {
	s := defaultSettings()
	wj := dialogueWJ(
		[]string{"Are@1.0-1.3", "you@1.3-1.5", "coming?@1.5-1.9"},
		[]string{"Yes.@2.1-2.4"},
		[]string{"Then@4.0-4.3", "hurry@4.3-4.6", "up,@4.6-4.8", "the@4.8-4.9", "train@4.9-5.2", "leaves@5.2-5.5", "in@5.5-5.6", "ten@5.6-5.8", "minutes@5.8-6.2", "and@6.2-6.3", "we@6.3-6.4", "still@6.4-6.6", "need@6.6-6.8", "tickets.@6.8-7.2"},
		[]string{"Fine.@7.4-7.8"},
	)
	turns := []speakerTurn{
		{Start: 0.9, End: 2.0, Speaker: 0},
		{Start: 2.0, End: 2.6, Speaker: 1},
		{Start: 3.9, End: 7.3, Speaker: 0},
		{Start: 7.3, End: 8.0, Speaker: 1},
	}
	srt := buildSRT(wj, 0, s, turns)
	cues := strings.Split(strings.TrimSpace(srt), "\n\n")
	if len(cues) != 3 {
		t.Fatalf("want 3 cues, got %d:\n%s", len(cues), srt)
	}
	if !strings.HasSuffix(cues[0], "\n- Are you coming?\n- Yes.") {
		t.Errorf("a short exchange must share one cue with dashes:\n%s", cues[0])
	}
	if strings.Contains(cues[1], "- ") || !strings.Contains(cues[1], "tickets.") || strings.Contains(cues[1], "Fine.") {
		t.Errorf("a long line stays alone, without dash, and the next voice gets its own cue:\n%s", cues[1])
	}
	if !strings.HasSuffix(cues[2], "\nFine.") {
		t.Errorf("third cue:\n%s", cues[2])
	}
}

func TestBuildSRTWithoutTurnsIsUnchanged(t *testing.T) {
	s := defaultSettings()
	wj := dialogueWJ([]string{"Are@1.0-1.3", "you@1.3-1.5", "coming?@1.5-1.9"}, []string{"Yes.@2.1-2.4"})
	srt := buildSRT(wj, 0, s, nil)
	if strings.Contains(srt, "- ") || strings.Count(srt, "-->") != 1 {
		t.Errorf("without speaker recognition nothing may change:\n%s", srt)
	}
	// One voice only (a monologue): no dashes, same cues as without.
	if one := buildSRT(wj, 0, s, []speakerTurn{{Start: 0, End: 9, Speaker: 0}}); one != srt {
		t.Errorf("a single speaker must give the same subtitle:\n%s\nvs\n%s", one, srt)
	}
}

func TestMergeDialogueRespectsLimits(t *testing.T) {
	s := defaultSettings()
	a := cue{start: 1, end: 2, lines: []string{"Hi."}, speaker: 0}
	b := cue{start: 2.2, end: 3, lines: []string{"Hello."}, speaker: 1}
	if got := mergeDialogue([]cue{a, b}, s); len(got) != 1 {
		t.Fatalf("should merge: %+v", got)
	}
	far := b
	far.start, far.end = 2+float64(s.subGapSplitMs)/1000+0.1, 9
	if got := mergeDialogue([]cue{a, far}, s); len(got) != 2 {
		t.Error("a long pause must keep two cues")
	}
	same := b
	same.speaker = 0
	if got := mergeDialogue([]cue{a, same}, s); len(got) != 2 {
		t.Error("the same voice is never shown as dialogue")
	}
	long := b
	long.lines = []string{strings.Repeat("x", s.subMaxCharsPerLine-1)}
	if got := mergeDialogue([]cue{a, long}, s); len(got) != 2 {
		t.Error("a line that cannot take the dash must stay alone")
	}
	// The tail of a longer speech, or the start of one, is no exchange.
	before := cue{start: 0, end: 0.9, lines: []string{"So we went home and"}, speaker: 0}
	after := cue{start: 3.1, end: 4, lines: []string{"is what I said."}, speaker: 1}
	if got := mergeDialogue([]cue{before, a, b}, s); len(got) != 3 {
		t.Error("a cut-off end of a speech must not become a dialogue line")
	}
	if got := mergeDialogue([]cue{a, b, after}, s); len(got) != 3 {
		t.Error("the start of a longer speech must not become a dialogue line")
	}
	s.subMaxLines = 1
	if got := mergeDialogue([]cue{a, b}, s); len(got) != 2 {
		t.Error("one line per subtitle leaves no room for dialogue")
	}
}

func TestParseSpeakerResult(t *testing.T) {
	res, err := parseSpeakerResult([]byte(`{"changes":[4.2,6.95],"turns":[{"start":0,"end":4.2,"speaker":0},{"start":4.2,"end":6.95,"speaker":1},{"start":6.95,"end":9,"speaker":0}]}`))
	if err != nil || len(res.Changes) != 2 || len(res.Turns) != 3 || res.Turns[1].Speaker != 1 || res.Turns[1].Start != 4.2 {
		t.Fatalf("parseSpeakerResult = %+v, %v", res, err)
	}
	sh := shiftTurns([]speakerTurn{{Start: 5, End: 6, Speaker: 1}, {Start: 1, End: 2, Speaker: 0}}, 0.2)
	if sh[0].Start != 0.8 || sh[1].Start != 4.8 {
		t.Errorf("shiftTurns must remove the lead-in and sort: %+v", sh)
	}
}

func TestChineseIsJoinedWithoutSpacesAndWrapsBetweenCharacters(t *testing.T) {
	s := defaultSettings()
	s.subMaxCharsPerLine = 16
	// faster-whisper's Chinese words: no leading space, except where the
	// speaker paused (" 说").
	wj := &whisperJSON{Segments: []whisperSegment{{Words: []whisperWord{
		{Start: 0.2, End: 0.5, Word: "这是"}, {Start: 0.5, End: 0.8, Word: "一个"},
		{Start: 0.8, End: 1.0, Word: "测"}, {Start: 1.0, End: 1.2, Word: "试，"},
		{Start: 1.5, End: 1.7, Word: " 说"}, {Start: 1.7, End: 1.9, Word: "话"},
		{Start: 1.9, End: 2.1, Word: "人"}, {Start: 2.1, End: 2.3, Word: "日"},
		{Start: 2.3, End: 2.5, Word: "志"}, {Start: 2.5, End: 2.7, Word: "的"},
		{Start: 2.7, End: 2.9, Word: "测试"}, {Start: 2.9, End: 3.1, Word: "音频。"},
	}}}}
	srt := buildSRT(wj, 0, s, nil)
	if !strings.Contains(srt, "这是一个测试，") || !strings.Contains(srt, "说话人日志的") {
		t.Fatalf("Chinese words must be joined without spaces:\n%s", srt)
	}
	for _, l := range strings.Split(srt, "\n") {
		if runeLen(l) > 16 && !strings.Contains(l, "-->") {
			t.Errorf("line longer than 16 characters: %q", l)
		}
		if strings.HasPrefix(l, "，") || strings.HasPrefix(l, "。") {
			t.Errorf("a line must not start with punctuation: %q", l)
		}
	}
	got := wrapText("这是一个测试，说话人日志的测试音频。", 10, 2)
	if len(got) != 2 || strings.Join(got, "") != "这是一个测试，说话人日志的测试音频。" {
		t.Errorf("wrapText(Chinese) = %q", got)
	}
}

func TestSpacedLanguagesKeepTheirSpaces(t *testing.T) {
	w := wordTS{text: "world", space: false} // even without the engine's space
	if got := joinWord("Hello", w); got != "Hello world" {
		t.Errorf("joinWord = %q", got)
	}
	if got := joinWord("日本", wordTS{text: "語"}); got != "日本語" {
		t.Errorf("joinWord(ja) = %q", got)
	}
	if got := joinWord("这是", wordTS{text: "说", space: true}); got != "这是 说" {
		t.Errorf("the engine's own space in Chinese stays: %q", got)
	}
	if got := wrapText("Grüße aus Köln, sagte der Bäcker zur Kundin am Morgen", 30, 2); strings.Join(got, " ") != "Grüße aus Köln, sagte der Bäcker zur Kundin am Morgen" {
		t.Errorf("wrapText(de) = %q", got)
	}
}

// wordsAt makes one engine segment of the given words, 0.3 s each, no pauses.
func wordsAt(text string) *whisperJSON {
	var seg whisperSegment
	t := 1.0
	for _, w := range strings.Fields(text) {
		seg.Words = append(seg.Words, whisperWord{Start: t, End: t + 0.3, Word: " " + w})
		t += 0.3
	}
	return &whisperJSON{Segments: []whisperSegment{seg}}
}

func cueTexts(srt string) []string {
	var out []string
	for _, block := range strings.Split(strings.TrimSpace(srt), "\n\n") {
		lines := strings.Split(block, "\n")
		out = append(out, strings.Join(lines[2:], " "))
	}
	return out
}

func TestCuesEndAtSentenceEnds(t *testing.T) {
	s := defaultSettings()
	got := cueTexts(buildSRT(wordsAt("We should leave before the storm arrives tonight. "+
		"The roads in the north will close at eight and nobody can get through after that."), 0, s, nil))
	if len(got) < 2 || got[0] != "We should leave before the storm arrives tonight." {
		t.Errorf("the first sentence must be a subtitle of its own: %q", got)
	}
	short := cueTexts(buildSRT(wordsAt("Yes. I know. But we have to hurry now."), 0, s, nil))
	if len(short) != 1 {
		t.Errorf("very short sentences stay together: %q", short)
	}
}

func TestOverlongCueIsCutAfterTheLastSentence(t *testing.T) {
	s := defaultSettings()
	got := cueTexts(buildSRT(wordsAt("It was late, and nobody wanted to go. "+
		"Then the phone rang twice and everybody in the room turned around at once"), 0, s, nil))
	if got[0] != "It was late, and nobody wanted to go." {
		t.Errorf("the cut must come after the sentence, not mid-way into the next: %q", got)
	}
	for _, c := range got {
		if strings.HasSuffix(c, " Then") {
			t.Errorf("a subtitle ends with the first word of the next sentence: %q", c)
		}
	}
}

func TestLinesNeverExceedTheLimit(t *testing.T) {
	s := defaultSettings()
	srt := buildSRT(wordsAt("Labor Day has marked a real shift in strategy for Hillary Clinton today talking "+
		"to reporters and launching attack after attack against Donald Trump for not releasing his tax returns"), 0, s, nil)
	for _, l := range strings.Split(srt, "\n") {
		if runeLen(l) > s.subMaxCharsPerLine && !strings.Contains(l, "-->") {
			t.Errorf("line longer than %d: %q", s.subMaxCharsPerLine, l)
		}
	}
}

func TestLineBreakPrefersPunctuation(t *testing.T) {
	got := wrapText("When the train finally arrived, everybody on the platform cheered", 42, 2)
	if len(got) != 2 || got[0] != "When the train finally arrived," {
		t.Errorf("the line should break after the comma: %q", got)
	}
}
