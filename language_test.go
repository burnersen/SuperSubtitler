//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import "testing"

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"de":      "de",
		"GER":     "de",
		"deutsch": "de",
		"en-US":   "en",
		"eng":     "en",
		"fre":     "fr",
		"spa":     "es",
		"jpn":     "ja",
		" it ":    "it",
	}
	for in, want := range cases {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}

// These paths must decide without ever reading stdin.
func TestDecideLanguageWithoutQuestions(t *testing.T) {
	cases := []struct {
		name          string
		cfg           AppConfig
		tag, detected string
		want          string
	}{
		{"forced wins", AppConfig{forcedLang: "ES"}, "de", "en", "es"},
		{"agree", AppConfig{}, "de", "de", "de"},
		{"detected only", AppConfig{}, "", "fr", "fr"},
		{"tag only", AppConfig{}, "it", "", "it"},
		{"mismatch translate keeps detected", AppConfig{translate: true}, "de", "en", "en"},
		{"mismatch auto keeps detected", AppConfig{auto: true}, "de", "en", "en"},
		{"unknown translate lets engine guess", AppConfig{translate: true}, "", "", ""},
		{"unknown auto lets engine guess", AppConfig{auto: true}, "", "", ""},
	}
	for _, c := range cases {
		cfg := c.cfg
		if willAskLanguage(&cfg, c.tag, c.detected) {
			t.Errorf("%s: willAskLanguage = true, want false", c.name)
			continue
		}
		if got := decideLanguage(&cfg, c.tag, c.detected, "x.mkv"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWillAskLanguage(t *testing.T) {
	cfg := &AppConfig{}
	if !willAskLanguage(cfg, "deu", "en") {
		t.Error("mismatch should ask")
	}
	if !willAskLanguage(cfg, "", "") {
		t.Error("unknown should ask")
	}
	if willAskLanguage(cfg, "ger", "de") {
		t.Error("ger and de are the same language")
	}
}

func TestBuildPlan(t *testing.T) {
	p := buildPlan(&AppConfig{}, "de")
	if p.task != "transcribe" || p.outLang != "de" || p.model != appSettings.modelTranscribe {
		t.Errorf("transcribe plan = %+v", p)
	}
	if p := buildPlan(&AppConfig{}, ""); p.outLang != "und" {
		t.Errorf("unknown source should be tagged und, got %q", p.outLang)
	}
	p = buildPlan(&AppConfig{translate: true}, "fr")
	if p.task != "translate" || p.outLang != "en" || p.srcLang != "fr" || p.model != appSettings.modelTranslate {
		t.Errorf("translate plan = %+v", p)
	}
}

func TestVoteForChunks(t *testing.T) {
	langs := map[string]string{"a": "de", "b": "en", "c": "de", "x": "fr"}
	if got := voteForChunks(langs, []string{"a", "b", "c"}); got != "de" {
		t.Errorf("majority = %q, want de", got)
	}
	if got := voteForChunks(langs, []string{"b", "x"}); got != "en" {
		t.Errorf("tie should break alphabetically, got %q", got)
	}
	if got := voteForChunks(langs, []string{"missing"}); got != "" {
		t.Errorf("no votes should be empty, got %q", got)
	}
}
