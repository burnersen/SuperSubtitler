//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTargets(t *testing.T) {
	ok, bad := parseTargets(" DE,fr,,de, german ")
	if !reflect.DeepEqual(ok, []string{"de", "fr"}) || !reflect.DeepEqual(bad, []string{" german "}) {
		t.Errorf("parseTargets = %q, %q", ok, bad)
	}
}

func c(start, end float64, speaker int, lines ...string) cue {
	return cue{start: start, end: end, speaker: speaker, lines: lines}
}

func TestTranslationUnitsFollowSentences(t *testing.T) {
	cues := []cue{
		c(1, 3, noSpeaker, "When the train finally arrived"),
		c(3, 5, noSpeaker, "at the station, everybody cheered."),
		c(6, 7, noSpeaker, "Good."),
		c(8, 9, 0, "- Coming?", "- Yes."),
	}
	units := translationUnits(cues)
	if len(units) != 4 {
		t.Fatalf("units = %+v", units)
	}
	if !reflect.DeepEqual(units[0].cues, []int{0, 1}) || units[0].text != "When the train finally arrived at the station, everybody cheered." {
		t.Errorf("a sentence over two cues is one unit: %+v", units[0])
	}
	if units[2].line != 0 || units[2].text != "Coming?" || units[3].line != 1 || units[3].text != "Yes." {
		t.Errorf("dialogue lines are translated one by one, without dashes: %+v %+v", units[2], units[3])
	}
}

func TestDistributeFollowsTheOriginalShares(t *testing.T) {
	parts := distribute("Als der Zug endlich im Bahnhof ankam, jubelten alle.", []int{30, 35}, 84)
	if len(parts) != 2 || parts[0] != "Als der Zug endlich im Bahnhof ankam," || parts[1] != "jubelten alle." {
		t.Errorf("cut should come after the comma near the middle: %q", parts)
	}
	parts = distribute("Ja.", []int{10, 10, 10}, 84)
	if strings.Join(parts, "") != "Ja." {
		t.Errorf("no text may be lost: %q", parts)
	}
	long := distribute("Sie hoffen wahrscheinlich, dass diese Umfrage ein Ausreißer ist und dass andere Umfragezahlen eine genauere und günstigere Momentaufnahme des Rennens geben werden.", []int{40, 70}, 84)
	if runeLen(long[1]) > 84 || runeLen(long[0]) > 84 {
		t.Errorf("no part may get more than fits: %q", long)
	}
	zh := distribute("当火车终于到站时，大家都欢呼起来。", []int{1, 1}, 0)
	if strings.Join(zh, "") != "当火车终于到站时，大家都欢呼起来。" || zh[0] != "当火车终于到站时，" {
		t.Errorf("Chinese splits between characters, after the comma: %q", zh)
	}
}

func TestApplyTranslationsKeepsTimesAndDashes(t *testing.T) {
	s := defaultSettings()
	cues := []cue{
		c(1, 3, noSpeaker, "When the train finally arrived"),
		c(3, 5, noSpeaker, "at the station, everybody cheered."),
		c(8, 9, 0, "- Coming?", "- Yes."),
	}
	units := translationUnits(cues)
	out := applyTranslations(cues, units, []string{"Als der Zug endlich im Bahnhof ankam, jubelten alle.", "Kommst du?", "Ja."}, s)
	if len(out) != 3 || out[0].start != 1 || out[1].start != 3 || out[2].start != 8 {
		t.Fatalf("times must stay: %+v", out)
	}
	if strings.Join(out[0].lines, " ")+" "+strings.Join(out[1].lines, " ") != "Als der Zug endlich im Bahnhof ankam, jubelten alle." {
		t.Errorf("text shared out wrongly: %q / %q", out[0].lines, out[1].lines)
	}
	if !reflect.DeepEqual(out[2].lines, []string{"- Kommst du?", "- Ja."}) {
		t.Errorf("dialogue = %q", out[2].lines)
	}
}

func TestModelsNeededForTranslation(t *testing.T) {
	s := appSettings
	defer func() { appSettings = s }()
	appSettings.detectModel, appSettings.modelTranscribe = "turbo", "turbo"
	got := modelsNeeded(&AppConfig{translateTo: []string{"de"}})
	if !reflect.DeepEqual(got, []string{"turbo", translationModel, translationTokenizer}) {
		t.Errorf("-to:de = %q", got)
	}
	if got := modelsNeeded(&AppConfig{setup: true}); len(got) != 2 {
		t.Errorf("-setup must not fetch the 3 GB translation model: %q", got)
	}
	for _, m := range []string{translationModel, translationTokenizer} {
		if pm, ok := pins.Models[m]; !ok || !pm.Mirror || len(pm.Revision) != 40 {
			t.Errorf("%s not pinned and mirrored: %+v", m, pm)
		}
	}
	if cfg := parseArgs([]string{"-to:de,FR", "-to:xx1"}); !reflect.DeepEqual(cfg.translateTo, []string{"de", "fr"}) || len(cfg.badTargets) != 1 {
		t.Errorf("-to parsing: %+v", cfg)
	}
}

// Translated subtitles keep the original's times: they may not hang on into
// a silence either, however much longer the translation is.
func TestTranslationIsNotSticky(t *testing.T) {
	s := defaultSettings()
	wj := &whisperJSON{Segments: []whisperSegment{
		{Words: []whisperWord{{Start: 1.0, End: 1.4, Word: " Hello."}}},
		{Words: []whisperWord{{Start: 21.0, End: 21.3, Word: " Are"}, {Start: 21.3, End: 21.5, Word: " you"}, {Start: 21.5, End: 22.0, Word: " there?"}}},
	}}
	cues := buildCues(wj, 0, s, nil)
	units := translationUnits(cues)
	out := applyTranslations(cues, units, []string{"Hallo und herzlich willkommen, schön, dass Sie da sind.", "Sind Sie wirklich noch da drüben?"}, s)
	checkNotSticky(t, out, wj, s)
}
