//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

type cue struct {
	start   float64
	end     float64
	lines   []string
	speaker int // noSpeaker without speaker marking
}

// wordTS is a lead-in-corrected word with absolute timing.
type wordTS struct {
	start   float64
	end     float64
	text    string
	speaker int  // noSpeaker without speaker marking
	space   bool // the engine put a space before it (see joinWord)
}

// buildSRT turns engine output into a finished SRT string. leadinSec (the
// artificial run-up silence) is removed from every timestamp so the result is
// frame-accurate against the original. turns (speaker recognition, in the
// WAV's timeline like the words) may be nil: then no speaker is marked.
func buildSRT(wj *whisperJSON, leadinSec float64, s AppSettings, turns []speakerTurn) string {
	words := collectWords(wj, leadinSec)
	if len(words) == 0 {
		return ""
	}
	if len(turns) > 0 {
		assignSpeakers(words, shiftTurns(turns, leadinSec))
	}
	cues := groupCues(words, s)
	cues = mergeDialogue(cues, s)
	enforceTiming(cues, s)
	return renderSRT(cues)
}

func collectWords(wj *whisperJSON, leadinSec float64) []wordTS {
	var out []wordTS
	for _, seg := range wj.Segments {
		if len(seg.Words) == 0 {
			t := cleanText(seg.Text)
			if t == "" {
				continue
			}
			out = append(out, shiftWord(wordTS{seg.Start, seg.End, t, noSpeaker, true}, leadinSec))
			continue
		}
		for _, w := range seg.Words {
			t := cleanText(w.Word)
			if t == "" {
				continue
			}
			out = append(out, shiftWord(wordTS{w.Start, w.End, t, noSpeaker, startsWithSpace(w.Word)}, leadinSec))
		}
	}
	// Drop anything that ended before t=0 after the shift; clamp the rest.
	var kept []wordTS
	for _, w := range out {
		if w.end <= 0 {
			continue
		}
		if w.start < 0 {
			w.start = 0
		}
		kept = append(kept, w)
	}
	return kept
}

func startsWithSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

// gluedScript: writing systems without spaces between words — Chinese,
// Japanese, Thai, Lao, Khmer, Myanmar. Their words are joined as they are;
// a space between them only where the engine itself put one (in Chinese
// subtitles a space stands for a short pause).
func gluedScript(r rune) bool {
	return breakableScript(r) || unicode.In(r, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

// breakableScript: a line may break between any two of these characters
// (Chinese characters, kana, CJK punctuation and full-width forms).
func breakableScript(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) ||
		(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// joinWord appends a word to text: with a space between words of spaced
// languages, without one between two words of a glued script.
func joinWord(text string, w wordTS) string {
	if text == "" {
		return w.text
	}
	last, _ := utf8.DecodeLastRuneInString(text)
	first, _ := utf8.DecodeRuneInString(w.text)
	if !w.space && gluedScript(last) && gluedScript(first) {
		return text + w.text
	}
	return text + " " + w.text
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func shiftWord(w wordTS, leadinSec float64) wordTS {
	w.start -= leadinSec
	w.end -= leadinSec
	return w
}

// groupCues greedily packs words into cues, breaking on long pauses, when the
// voice changes, or when the next word would exceed the character or duration
// budget.
func groupCues(words []wordTS, s AppSettings) []cue {
	maxChars := s.subMaxCharsPerLine * s.subMaxLines
	maxDur := float64(s.subMaxDurMs) / 1000.0
	gapSplit := float64(s.subGapSplitMs) / 1000.0

	var cues []cue
	cur := ""
	var curStart, curEnd float64
	curSpeaker := noSpeaker

	flush := func() {
		if cur == "" {
			return
		}
		cues = append(cues, cue{
			start:   curStart,
			end:     curEnd,
			lines:   wrapText(cur, s.subMaxCharsPerLine, s.subMaxLines),
			speaker: curSpeaker,
		})
		cur = ""
	}

	for _, w := range words {
		if cur == "" {
			cur = w.text
			curStart, curEnd, curSpeaker = w.start, w.end, w.speaker
			continue
		}
		gap := w.start - curEnd
		next := joinWord(cur, w)
		if gap > gapSplit || runeLen(next) > maxChars || (w.end-curStart) > maxDur || w.speaker != curSpeaker {
			flush()
			cur = w.text
			curStart, curEnd, curSpeaker = w.start, w.end, w.speaker
			continue
		}
		cur = next
		curEnd = w.end
	}
	flush()
	return cues
}

// dialoguePrefix starts each line of a subtitle shared by two speakers.
const dialoguePrefix = "- "

// mergeDialogue puts two short exchanges of different speakers into one
// subtitle, one "- " line each — the usual way (Netflix and others) to show a
// quick back-and-forth that would otherwise flash by as two tiny subtitles.
// Only when each is everything that voice says at that point (not the tail or
// start of a longer speech, which would read as a cut-off sentence), both fit
// on one line each (dash included), the subtitle may have two lines, the pause
// between them is no longer than the one that starts a new subtitle anyway,
// and together they stay within the longest display time.
func mergeDialogue(cues []cue, s AppSettings) []cue {
	if s.subMaxLines < 2 {
		return cues
	}
	maxDur := float64(s.subMaxDurMs) / 1000.0
	gapSplit := float64(s.subGapSplitMs) / 1000.0
	fits := func(c cue) bool {
		return c.speaker != noSpeaker && len(c.lines) == 1 &&
			len([]rune(dialoguePrefix+c.lines[0])) <= s.subMaxCharsPerLine
	}
	// continues: cue j carries on what cue i (right before it) said.
	continues := func(i, j int) bool {
		return i >= 0 && j < len(cues) && cues[i].speaker == cues[j].speaker &&
			cues[j].start-cues[i].end <= gapSplit
	}
	var out []cue
	for i := 0; i < len(cues); i++ {
		a := cues[i]
		if i+1 < len(cues) {
			b := cues[i+1]
			if fits(a) && fits(b) && a.speaker != b.speaker &&
				!continues(i-1, i) && !continues(i+1, i+2) &&
				b.start-a.end <= gapSplit && b.end-a.start <= maxDur {
				out = append(out, cue{
					start:   a.start,
					end:     b.end,
					lines:   []string{dialoguePrefix + a.lines[0], dialoguePrefix + b.lines[0]},
					speaker: b.speaker,
				})
				i++
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// enforceTiming applies minimum duration, minimum gap (anti-flicker) and a soft
// CPS cap by extending cue ends into available space.
func enforceTiming(cues []cue, s AppSettings) {
	minDur := float64(s.subMinDurMs) / 1000.0
	minGap := float64(s.subMinGapMs) / 1000.0
	maxDur := float64(s.subMaxDurMs) / 1000.0

	for i := range cues {
		if cues[i].end < cues[i].start {
			cues[i].end = cues[i].start
		}
		// Minimum duration.
		if cues[i].end-cues[i].start < minDur {
			cues[i].end = cues[i].start + minDur
		}
		// CPS: extend end to slow fast cues, bounded by maxDur.
		chars := float64(cueChars(cues[i]))
		if dur := cues[i].end - cues[i].start; dur > 0 && chars/dur > s.subMaxCPS {
			want := cues[i].start + chars/s.subMaxCPS
			if want > cues[i].start+maxDur {
				want = cues[i].start + maxDur
			}
			if want > cues[i].end {
				cues[i].end = want
			}
		}
		// Minimum gap to the next cue (trim, never overlap).
		if i+1 < len(cues) {
			limit := cues[i+1].start - minGap
			if cues[i].end > limit {
				if limit > cues[i].start {
					cues[i].end = limit
				} else {
					cues[i].end = cues[i].start + 0.001
				}
			}
		}
	}
}

func cueChars(c cue) int {
	n := 0
	for _, l := range c.lines {
		n += len([]rune(l))
	}
	return n
}

// wrapText splits text into at most maxLines lines of at most perLine chars,
// balancing the two-line case for readability. Lines break at spaces, and in
// Chinese and Japanese between any two characters.
func wrapText(text string, perLine, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if maxLines <= 1 || runeLen(text) <= perLine {
		return []string{text}
	}
	units := splitUnits(text)
	if maxLines == 2 {
		if l := balancedSplit(units, perLine); l != nil {
			return l
		}
	}
	// Greedy first-fit for the general case; nothing is ever dropped.
	var lines []string
	start := 0
	for i := 1; i < len(units); i++ {
		if runeLen(joinUnits(units[start:i+1])) > perLine {
			lines = append(lines, joinUnits(units[start:i]))
			start = i
		}
	}
	lines = append(lines, joinUnits(units[start:]))
	// Fold any overflow beyond maxLines into the last allowed line.
	if len(lines) > maxLines {
		head := append([]string{}, lines[:maxLines-1]...)
		tail := strings.Join(lines[maxLines-1:], " ")
		lines = append(head, tail)
	}
	return lines
}

// unit is the smallest piece a line may break before; space tells whether a
// space separates it from the unit before.
type unit struct {
	text  string
	space bool
}

// splitUnits cuts text at its spaces, and Chinese/Japanese runs into single
// characters. Punctuation stays with the character before it, so no line
// starts with "，" or "。".
func splitUnits(text string) []unit {
	var out []unit
	for _, field := range strings.Fields(text) {
		first := true
		for _, r := range field {
			n := len(out)
			glue := !first && n > 0 && (unicode.IsPunct(r) ||
				!breakableScript(r) && !breakableLast(out[n-1].text))
			if glue {
				out[n-1].text += string(r)
			} else {
				out = append(out, unit{text: string(r), space: first})
			}
			first = false
		}
	}
	if len(out) > 0 {
		out[0].space = false
	}
	return out
}

func breakableLast(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return breakableScript(r)
}

func joinUnits(units []unit) string {
	var b strings.Builder
	for i, u := range units {
		if i > 0 && u.space {
			b.WriteByte(' ')
		}
		b.WriteString(u.text)
	}
	return b.String()
}

// balancedSplit finds a two-line split minimising the longer line, where both
// lines fit perLine. A break at a space wins over one between two characters
// (in Chinese a space marks a pause, the natural place for the break).
// Returns nil if no split fits.
func balancedSplit(units []unit, perLine int) []string {
	for _, atSpaceOnly := range []bool{true, false} {
		best := -1
		bestScore := 1 << 30
		for i := 1; i < len(units); i++ {
			if atSpaceOnly && !units[i].space {
				continue
			}
			l1 := runeLen(joinUnits(units[:i]))
			l2 := runeLen(joinUnits(units[i:]))
			if l1 > perLine || l2 > perLine {
				continue
			}
			score := l1
			if l2 > l1 {
				score = l2
			}
			if score < bestScore {
				bestScore = score
				best = i
			}
		}
		if best >= 0 {
			return []string{joinUnits(units[:best]), joinUnits(units[best:])}
		}
	}
	return nil
}

func renderSRT(cues []cue) string {
	var b strings.Builder
	idx := 1
	for _, c := range cues {
		text := strings.TrimSpace(strings.Join(c.lines, "\n"))
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", idx, srtTime(c.start), srtTime(c.end), text)
		idx++
	}
	return b.String()
}

func srtTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(sec * 1000.0)
	ms := total % 1000
	total /= 1000
	s := total % 60
	total /= 60
	m := total % 60
	h := total / 60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// cleanText strips engine artefacts and collapses whitespace in a single token
// or segment string.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\u200b' || r == '\ufeff' || r == '\u00a0' {
			return -1
		}
		if r < 0x20 && r != '\n' {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func writeSRT(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("Subtitle.go: writeSRT: %w", err)
	}
	return nil
}
