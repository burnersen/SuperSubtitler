//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"strings"
	"testing"
)

func TestParseCleanerRules(t *testing.T) {
	r := parseCleanerRules("# comment\n; also comment\n\nUntertitel\n=2017\n=  \n")
	if len(r.substr) != 1 || r.substr[0] != "untertitel" {
		t.Errorf("substr = %q", r.substr)
	}
	if len(r.exact) != 1 || r.exact[0] != "2017" {
		t.Errorf("exact = %q", r.exact)
	}
}

func TestCleanSRTDropsMatchesAndRenumbers(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\nUntertitel von XYZ\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nIm Jahr 2017\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\n2017\n\n" +
		"4\n00:00:07,000 --> 00:00:08,000\nBleibt\n\n"
	got := cleanSRT(srt, parseCleanerRules("untertitel\n=2017\n"))
	want := "1\n00:00:03,000 --> 00:00:04,000\nIm Jahr 2017\n\n" +
		"2\n00:00:07,000 --> 00:00:08,000\nBleibt\n\n"
	if got != want {
		t.Fatalf("cleanSRT:\n%s\nwant:\n%s", got, want)
	}
}

func TestCleanSRTHandlesCRLF(t *testing.T) {
	srt := strings.ReplaceAll("1\n00:00:01,000 --> 00:00:02,000\nKeep\n\n", "\n", "\r\n")
	if got := cleanSRT(srt, cleanerRules{}); !strings.Contains(got, "Keep") {
		t.Fatalf("CRLF cue lost: %q", got)
	}
}
