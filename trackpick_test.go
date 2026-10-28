//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"reflect"
	"testing"
)

func stream(codec string, ch int, layout, lang, title string) probeStream {
	return probeStream{CodecType: "audio", CodecName: codec, Channels: ch, ChannelLayout: layout,
		Tags: map[string]string{"language": lang, "title": title}}
}

func TestBestTrackPerLanguage(t *testing.T) {
	cands := []trackCandidate{
		{ordinal: 0, stream: stream("truehd", 8, "7.1", "ger", ""), lang: "de", clear: clarity{-0.30, 120}, scored: true},
		{ordinal: 1, stream: stream("ac3", 6, "5.1(side)", "ger", ""), lang: "de", clear: clarity{-0.20, 120}, scored: true},
		{ordinal: 2, stream: stream("ac3", 2, "stereo", "ger", "Audiodeskription"), lang: "de", clear: clarity{-0.10, 150}, scored: true},
		{ordinal: 3, stream: stream("dts", 6, "5.1(side)", "eng", ""), lang: "en", clear: clarity{-0.60, 110}, scored: true},
		{ordinal: 4, stream: stream("aac", 2, "stereo", "eng", ""), lang: "en", clear: clarity{-0.25, 115}, scored: true},
		{ordinal: 5, stream: stream("aac", 2, "stereo", "eng", "Director's Commentary"), lang: "en", clear: clarity{-0.15, 90}, scored: true},
	}
	// German: the AC3 is understood clearly better than the TrueHD; the audio
	// description, though clearest, is never suggested. English: the stereo
	// track beats the muddy DTS; the commentary is left out.
	if got := bestPerLanguage(cands); !reflect.DeepEqual(got, []int{1, 4}) {
		t.Errorf("best = %v, want [1 4]", got)
	}
}

func TestEqualClarityGoesToCentreLosslessBitrate(t *testing.T) {
	a := trackCandidate{ordinal: 0, stream: stream("aac", 2, "stereo", "ger", ""), lang: "de", clear: clarity{-0.20, 100}, scored: true}
	b := trackCandidate{ordinal: 1, stream: stream("ac3", 6, "5.1", "ger", ""), lang: "de", clear: clarity{-0.22, 100}, scored: true}
	c := trackCandidate{ordinal: 2, stream: stream("truehd", 6, "5.1", "ger", ""), lang: "de", clear: clarity{-0.21, 100}, scored: true}
	if got := bestPerLanguage([]trackCandidate{a, b, c}); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("within the tie window the centre channel, then lossless wins: %v", got)
	}
	// Without measurements (-lang given) the same rules still pick.
	a.scored, b.scored, c.scored = false, false, false
	if got := bestPerLanguage([]trackCandidate{a, b}); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("unmeasured: %v", got)
	}
}

func TestClarityOf(t *testing.T) {
	wj := &whisperJSON{Segments: []whisperSegment{
		{Start: 0, End: 4, Text: "hello there", AvgLogprob: -0.2, NoSpeechProb: 0.1},
		{Start: 4, End: 6, Text: "♪", AvgLogprob: -1.5, NoSpeechProb: 0.9},
		{Start: 6, End: 8, Text: "fine", AvgLogprob: -0.5, NoSpeechProb: 0.2},
	}}
	c := clarityOf(wj)
	if c.speech != 6 || c.logprob < -0.31 || c.logprob > -0.29 {
		t.Errorf("clarity = %+v (music must not count)", c)
	}
	tc, ok := trackClarity([]string{"a", "b"}, map[string]clarity{"a": {-0.2, 10}, "b": {-0.4, 30}})
	if !ok || tc.speech != 40 || tc.logprob > -0.349 || tc.logprob < -0.351 {
		t.Errorf("trackClarity = %+v", tc)
	}
	if !sideTrack(stream("ac3", 2, "", "ger", "Hörfilm-Fassung")) || sideTrack(stream("ac3", 6, "", "ger", "Deutsch DD 5.1")) {
		t.Error("sideTrack titles")
	}
}
