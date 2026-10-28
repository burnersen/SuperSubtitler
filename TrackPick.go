//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// TrackPick.go — which audio track is the best one of its language.
//
// Films often carry the same language several times (TrueHD 7.1, AC3 5.1,
// stereo, a commentary, an audio description). Every track of such a file is
// listened to during language detection anyway; how sure the speech model is
// of what it hears there (its average log-probability over the speech it
// found) is a direct measure of how well that track will transcribe. Per
// language the clearest track is suggested; when two are about equally clear,
// a centre channel (dialogue on its own), a lossless format and a higher bit
// rate decide. Commentaries and audio descriptions are never suggested.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// clarity is how well the speech model understood a sample.
type clarity struct {
	logprob float64 // speech-time-weighted average log-probability (<= 0)
	speech  float64 // seconds of speech found
}

// clarityOf measures one engine result: segments the model itself thinks
// are no speech are left out.
func clarityOf(wj *whisperJSON) clarity {
	var c clarity
	sum := 0.0
	for _, s := range wj.Segments {
		d := s.End - s.Start
		if d <= 0 || s.NoSpeechProb > 0.6 || strings.TrimSpace(s.Text) == "" {
			continue
		}
		sum += s.AvgLogprob * d
		c.speech += d
	}
	if c.speech > 0 {
		c.logprob = sum / c.speech
	}
	return c
}

// trackClarity combines the samples of one track.
func trackClarity(chunks []string, per map[string]clarity) (clarity, bool) {
	var c clarity
	sum := 0.0
	for _, ch := range chunks {
		if v, ok := per[ch]; ok && v.speech > 0 {
			sum += v.logprob * v.speech
			c.speech += v.speech
		}
	}
	if c.speech == 0 {
		return c, false
	}
	c.logprob = sum / c.speech
	return c, true
}

// sideTrack: a commentary, an audio description or karaoke — never the one
// to subtitle the film by.
func sideTrack(s probeStream) bool {
	if s.Disposition["comment"] == 1 || s.Disposition["visual_impaired"] == 1 || s.Disposition["karaoke"] == 1 {
		return true
	}
	title := strings.ToLower(s.Tags["title"])
	for _, w := range []string{"comment", "kommentar", "commentaire", "commento", "comentario",
		"description", "deskription", "beschreibung", "hörfilm", "hoerfilm", "sehbehindert",
		"visually impaired", "audiodescri", "karaoke"} {
		if strings.Contains(title, w) {
			return true
		}
	}
	return false
}

// trackCandidate is one audio track as the ranking sees it.
type trackCandidate struct {
	ordinal int
	stream  probeStream
	lang    string // detected, else the container tag, else "und"
	clear   clarity
	scored  bool
}

// clarityTie: log-probability differences smaller than this count as equal
// (two encodings of the same mix land within a hundredth or two; music or
// noise over the speech costs more).
const clarityTie = 0.03

func hasCentre(s probeStream) bool {
	return s.Channels >= 6 || strings.Contains(s.ChannelLayout, "5.1") || strings.Contains(s.ChannelLayout, "7.1")
}

func lossless(s probeStream) bool {
	c := strings.ToLower(s.CodecName)
	return c == "truehd" || c == "flac" || c == "mlp" || strings.HasPrefix(c, "pcm_") ||
		(c == "dts" && strings.Contains(strings.ToLower(s.Profile), "ma"))
}

func bitRate(s probeStream) int {
	n, _ := strconv.Atoi(s.BitRate)
	return n
}

// better says whether a should be preferred over b (same language).
func better(a, b trackCandidate) bool {
	if a.scored != b.scored {
		return a.scored
	}
	if a.scored {
		if d := a.clear.logprob - b.clear.logprob; d > clarityTie || d < -clarityTie {
			return d > 0
		}
	}
	if hasCentre(a.stream) != hasCentre(b.stream) {
		return hasCentre(a.stream)
	}
	if lossless(a.stream) != lossless(b.stream) {
		return lossless(a.stream)
	}
	if bitRate(a.stream) != bitRate(b.stream) {
		return bitRate(a.stream) > bitRate(b.stream)
	}
	return a.ordinal < b.ordinal
}

// bestPerLanguage returns the ordinals of the best track of every language
// (side tracks only when a language has nothing else), in track order.
func bestPerLanguage(cands []trackCandidate) []int {
	best := map[string]trackCandidate{}
	for _, c := range cands {
		cur, ok := best[c.lang]
		switch {
		case !ok:
			best[c.lang] = c
		case sideTrack(cur.stream) && !sideTrack(c.stream):
			best[c.lang] = c
		case sideTrack(c.stream) && !sideTrack(cur.stream):
		case better(c, cur):
			best[c.lang] = c
		}
	}
	var out []int
	for _, c := range best {
		out = append(out, c.ordinal)
	}
	sort.Ints(out)
	return out
}

// candidateNote is the remark shown next to a track in the question.
func candidateNote(c trackCandidate, best map[int]bool) string {
	var parts []string
	if best[c.ordinal] {
		parts = append(parts, "best "+c.lang)
	}
	if sideTrack(c.stream) {
		parts = append(parts, "commentary/description")
	}
	if c.scored {
		parts = append(parts, fmt.Sprintf("clarity %.0f%%", clarityPercent(c.clear.logprob)))
	} else if c.clear.speech == 0 && c.lang != "" {
		parts = append(parts, "no speech found")
	}
	return strings.Join(parts, " · ")
}

// clarityPercent shows a log-probability as a rough 0-100 % (the model's
// average per-token confidence).
func clarityPercent(logprob float64) float64 {
	p := 100 * math.Exp(logprob)
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// chooseTracks asks which of a file's audio tracks to subtitle, after all of
// them were analysed, and keeps only those.
func chooseTracks(cfg *AppConfig, job *fileJob) {
	cands := make([]trackCandidate, len(job.tracks))
	for i, t := range job.tracks {
		lang := t.detected
		if lang == "" {
			lang = containerLang(t.stream)
		}
		if lang == "" {
			lang = "und"
		}
		cands[i] = trackCandidate{ordinal: t.ordinal, stream: t.stream, lang: lang, clear: t.clear, scored: t.scored && t.err == nil}
	}
	pInfo.Printf("%s\n", job.label()) // context for the question
	var kept []*trackJob
	for _, i := range promptTrackSelection(cands, job.src, cfg.auto) {
		kept = append(kept, job.tracks[i])
	}
	job.tracks = kept
	emitFile(job, job.total)
}
