//go:build windows && amd64

package main

import (
	"fmt"
	"os"
	"strings"
)

type cue struct {
	start float64
	end   float64
	lines []string
}

// wordTS is a lead-in-corrected word with absolute timing.
type wordTS struct {
	start float64
	end   float64
	text  string
}

// buildSRT turns engine output into a finished SRT string. leadinSec (the
// artificial run-up silence) is removed from every timestamp so the result is
// frame-accurate against the original.
func buildSRT(wj *whisperJSON, leadinSec float64, s AppSettings) string {
	words := collectWords(wj, leadinSec)
	if len(words) == 0 {
		return ""
	}
	cues := groupCues(words, s)
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
			out = append(out, shiftWord(wordTS{seg.Start, seg.End, t}, leadinSec))
			continue
		}
		for _, w := range seg.Words {
			t := cleanText(w.Word)
			if t == "" {
				continue
			}
			out = append(out, shiftWord(wordTS{w.Start, w.End, t}, leadinSec))
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

func shiftWord(w wordTS, leadinSec float64) wordTS {
	w.start -= leadinSec
	w.end -= leadinSec
	return w
}

// groupCues greedily packs words into cues, breaking on long pauses or when the
// next word would exceed the character or duration budget.
func groupCues(words []wordTS, s AppSettings) []cue {
	maxChars := s.subMaxCharsPerLine * s.subMaxLines
	maxDur := float64(s.subMaxDurMs) / 1000.0
	gapSplit := float64(s.subGapSplitMs) / 1000.0

	var cues []cue
	var curWords []string
	var curStart, curEnd float64

	flush := func() {
		if len(curWords) == 0 {
			return
		}
		text := strings.Join(curWords, " ")
		cues = append(cues, cue{
			start: curStart,
			end:   curEnd,
			lines: wrapText(text, s.subMaxCharsPerLine, s.subMaxLines),
		})
		curWords = nil
	}

	for _, w := range words {
		if len(curWords) == 0 {
			curWords = []string{w.text}
			curStart, curEnd = w.start, w.end
			continue
		}
		gap := w.start - curEnd
		tentative := len(strings.Join(curWords, " ")) + 1 + len(w.text)
		if gap > gapSplit || tentative > maxChars || (w.end-curStart) > maxDur {
			flush()
			curWords = []string{w.text}
			curStart, curEnd = w.start, w.end
			continue
		}
		curWords = append(curWords, w.text)
		curEnd = w.end
	}
	flush()
	return cues
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
// balancing the two-line case for readability.
func wrapText(text string, perLine, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if maxLines <= 1 || len([]rune(text)) <= perLine {
		return []string{text}
	}
	words := strings.Fields(text)
	if maxLines == 2 {
		if l := balancedSplit(words, perLine); l != nil {
			return l
		}
	}
	// Greedy first-fit for the general case; no word is ever dropped.
	var lines []string
	cur := ""
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if len([]rune(cur))+1+len([]rune(w)) <= perLine {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	// Fold any overflow beyond maxLines into the last allowed line.
	if len(lines) > maxLines {
		head := append([]string{}, lines[:maxLines-1]...)
		tail := strings.Join(lines[maxLines-1:], " ")
		lines = append(head, tail)
	}
	return lines
}

// balancedSplit finds a two-line split minimising the longer line, preferring
// splits where both lines fit perLine. Returns nil if no split fits.
func balancedSplit(words []string, perLine int) []string {
	best := -1
	bestScore := 1 << 30
	for i := 1; i < len(words); i++ {
		l1 := len([]rune(strings.Join(words[:i], " ")))
		l2 := len([]rune(strings.Join(words[i:], " ")))
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
	if best < 0 {
		return nil
	}
	return []string{
		strings.Join(words[:best], " "),
		strings.Join(words[best:], " "),
	}
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
