//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// Translate.go — subtitles in other languages (-to:de, -to:de,fr …).
//
// The finished subtitles of a track are translated by MADLAD-400 3B
// (transcribe.py --translate_text) and written as another subtitle file and
// MKV track with exactly the same timing. Whisper itself can only translate
// into English; MADLAD goes from any of some 400 languages into any other.
//
// The model needs context, a subtitle often holds half a sentence: so the
// text of consecutive subtitles up to a sentence end is translated as one
// unit, and the translation is shared out over those subtitles again in the
// proportions of the original. The two lines of a dialogue subtitle ("- ")
// are translated each on its own and keep their dashes.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The pinned model (runtime.json): MADLAD-400 3B as CTranslate2 int8, and
// its tokenizer, which the engine's tokenizers package reads.
const (
	translationModel     = "translation"
	translationTokenizer = "translation-tokenizer"
)

// gpuMu keeps the speech model and the translation model from loading onto
// the graphics card at the same time (translation runs in the finalize
// stage while the next file is being transcribed): one after the other
// fits on any card that fits either.
var gpuMu sync.Mutex

var targetCode = regexp.MustCompile(`^[a-z]{2,3}$`)

// parseTargets reads "-to:de,fr": lower-case language codes, each once.
func parseTargets(spec string) ([]string, []string) {
	var ok, bad []string
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		code := strings.ToLower(strings.TrimSpace(part))
		switch {
		case code == "":
		case !targetCode.MatchString(code):
			bad = append(bad, part)
		case !seen[code]:
			seen[code] = true
			ok = append(ok, code)
		}
	}
	return ok, bad
}

// transUnit is one piece of text translated as a whole: consecutive cues up
// to a sentence end, or one line of a dialogue cue (line >= 0).
type transUnit struct {
	cues []int
	line int
	text string
}

// maxUnitCues bounds a unit when no sentence end comes (a long run-on).
const maxUnitCues = 4

func isDialogue(c cue) bool {
	return len(c.lines) == 2 && strings.HasPrefix(c.lines[0], dialoguePrefix) && strings.HasPrefix(c.lines[1], dialoguePrefix)
}

func cueText(c cue) string {
	text := ""
	for _, l := range c.lines {
		text = joinWord(text, wordTS{text: l})
	}
	return text
}

// translationUnits splits the cues into the pieces that are translated.
func translationUnits(cues []cue) []transUnit {
	var units []transUnit
	var open []int
	flush := func() {
		if len(open) == 0 {
			return
		}
		text := ""
		for _, i := range open {
			text = joinWord(text, wordTS{text: cueText(cues[i])})
		}
		units = append(units, transUnit{cues: open, line: -1, text: text})
		open = nil
	}
	for i, c := range cues {
		if isDialogue(c) {
			flush()
			for l, line := range c.lines {
				units = append(units, transUnit{cues: []int{i}, line: l, text: strings.TrimPrefix(line, dialoguePrefix)})
			}
			continue
		}
		if len(open) > 0 && cues[open[len(open)-1]].speaker != c.speaker {
			flush()
		}
		open = append(open, i)
		if endsSentence(cueText(c)) || len(open) >= maxUnitCues {
			flush()
		}
	}
	flush()
	return units
}

// distribute cuts text into len(weights) parts whose lengths follow the
// weights, at word (or, in Chinese and Japanese, character) boundaries; near
// the ideal point a cut after punctuation is preferred, and a cut that leaves
// a part longer than capacity characters (0 = no limit) is avoided.
func distribute(text string, weights []int, capacity int) []string {
	parts := make([]string, len(weights))
	units := splitUnits(text)
	if len(weights) == 1 || len(units) == 0 {
		parts[0] = text
		return parts
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	lengths := make([]int, len(units)) // characters up to and including unit i
	n := 0
	for i, u := range units {
		n += runeLen(u.text)
		if i > 0 && u.space {
			n++
		}
		lengths[i] = n
	}
	start, acc := 0, 0
	for p := 0; p < len(weights)-1; p++ {
		acc += weights[p]
		ideal := float64(n) * float64(acc) / float64(total)
		best, bestScore := -1, 1e18
		// leave at least one unit for every part still to come
		for k := start; k < len(units)-(len(weights)-1-p); k++ {
			score := abs(float64(lengths[k]) - ideal)
			// A cut at a comma or sentence end within 25 % of the text from
			// the ideal point beats an exact one in mid-phrase.
			if (endsSentence(units[k].text) || endsClause(units[k].text)) && score <= float64(n)*0.25 {
				score -= float64(n) * 0.25
			}
			if capacity > 0 {
				before := 0
				if start > 0 {
					before = lengths[start-1] + 1
				}
				left := len(weights) - 1 - p
				if lengths[k]-before > capacity || n-lengths[k] > left*capacity {
					score += float64(n)
				}
			}
			if score < bestScore {
				best, bestScore = k, score
			}
		}
		if best < start {
			break
		}
		parts[p] = joinUnits(units[start : best+1])
		start = best + 1
	}
	parts[len(weights)-1] = joinUnits(units[start:])
	return parts
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// wrapTranslation wraps translated text. A translation is often longer than
// the original (German some 20-30 % over English) while the time is the
// same: rather one line more than lines running off the limit.
func wrapTranslation(text string, s AppSettings) []string {
	for lines := s.subMaxLines; lines <= s.subMaxLines+1; lines++ {
		l := wrapText(text, s.subMaxCharsPerLine, lines)
		fits := true
		for _, line := range l {
			if runeLen(line) > s.subMaxCharsPerLine {
				fits = false
			}
		}
		if fits {
			return l
		}
	}
	return wrapText(text, s.subMaxCharsPerLine, s.subMaxLines+1)
}

// applyTranslations builds the translated cues: same times, new lines.
func applyTranslations(cues []cue, units []transUnit, out []string, s AppSettings) []cue {
	res := make([]cue, len(cues))
	for i, c := range cues {
		res[i] = cue{start: c.start, end: c.end, speaker: c.speaker}
	}
	for u, unit := range units {
		text := strings.TrimSpace(out[u])
		if unit.line >= 0 {
			res[unit.cues[0]].lines = append(res[unit.cues[0]].lines, dialoguePrefix+text)
			continue
		}
		weights := make([]int, len(unit.cues))
		for k, ci := range unit.cues {
			weights[k] = runeLen(cueText(cues[ci])) + 1
		}
		for k, part := range distribute(text, weights, s.subMaxCharsPerLine*s.subMaxLines) {
			res[unit.cues[k]].lines = wrapTranslation(part, s)
		}
	}
	var kept []cue
	for _, c := range res {
		if strings.TrimSpace(strings.Join(c.lines, "")) != "" {
			kept = append(kept, c)
		}
	}
	enforceTiming(kept, s)
	return kept
}

// translateCues translates cues into target. onLine receives the driver's
// "@PROGRESS x" lines.
func translateCues(ctx context.Context, cues []cue, target, workDir string, s AppSettings, onLine func(string)) ([]cue, error) {
	units := translationUnits(cues)
	texts := make([]string, len(units))
	for i, u := range units {
		texts[i] = u.text
	}
	in := filepath.Join(workDir, "translate-"+target+".in.json")
	outPath := filepath.Join(workDir, "translate-"+target+".out.json")
	data, _ := json.Marshal(map[string][]string{"texts": texts})
	if err := os.WriteFile(in, data, 0644); err != nil {
		return nil, fmt.Errorf("Translate.go: write input: %w", err)
	}
	if err := runTranslator(ctx, in, outPath, target, onLine); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("Translate.go: read result: %w", err)
	}
	var res struct {
		Translations []string `json:"translations"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Translations) != len(units) {
		return nil, fmt.Errorf("Translate.go: unusable result (%d of %d texts): %v", len(res.Translations), len(units), err)
	}
	return applyTranslations(cues, units, res.Translations, s), nil
}

// runTranslator runs the driver on the graphics card when there is one and
// falls back to the processor once if that fails, like the speech model.
func runTranslator(ctx context.Context, in, out, target string, onLine func(string)) error {
	gpuMu.Lock()
	defer gpuMu.Unlock()
	device, _ := engineDevice(false)
	try := func(dev string) error {
		compute := "int8"
		if dev == "cuda" {
			compute = "int8_float16"
		}
		return runProcessStreaming(ctx, onLine, pythonExePath, whisperDriverPath, "--translate_text",
			"--model_dir", modelArg(translationModel),
			"--tokenizer", filepath.Join(modelArg(translationTokenizer), "tokenizer.json"),
			"--target", target, "--device", dev, "--compute_type", compute,
			"--input", in, "--output", out)
	}
	err := try(device)
	if err != nil && ctx.Err() == nil && device == "cuda" && looksLikeGPUError(err) {
		pWarn.Println("Translation on the graphics card failed — using the processor (slower). Detail: " + shortErr(err))
		err = try("cpu")
	}
	if err != nil {
		return fmt.Errorf("Translate.go: translate: %w", err)
	}
	return nil
}

// translateTrack writes the translations of one track's cues and reports
// them. It returns the subtitle files written (failures are warned about,
// the original subtitle is never lost over them).
func translateTrack(ctx context.Context, cfg *AppConfig, job *fileJob, i int, t *trackJob, cues []cue, base string, used map[string]bool) []subInput {
	var subs []subInput
	tlabel := trackLabelFor(job, i)
	for _, target := range cfg.translateTo {
		if target == t.outLang {
			continue // already in that language
		}
		emitStage(job.index+1, i+1, "translate")
		pInfo.Printf("%s — translating into %s...\n", tlabel, target)
		started := time.Now()
		lastEmit := -1.0
		onLine := func(line string) {
			f, ok := parseProgress(line)
			if !ok {
				return
			}
			if f-lastEmit >= 0.01 || f >= 1 {
				lastEmit = f
				emitEvent(eventProgress{
					Ev: "progress", Index: job.index + 1, Track: i + 1, Tracks: len(job.tracks),
					Pct: f, ETASec: etaSeconds(started, f), Stage: "translate",
				})
			}
		}
		translated, err := translateCues(ctx, cues, target, filepath.Dir(t.mainWav), appSettings, onLine)
		if err != nil {
			if ctx.Err() == nil {
				pWarn.Printf("%s — translation into %s failed: %s\n", tlabel, target, shortErr(err))
			}
			continue
		}
		path := uniqueSRTPath(base, target, used)
		if err := writeSRT(path, renderSRT(translated)); err != nil {
			pWarn.Printf("%s — %v\n", tlabel, err)
			continue
		}
		pInfo.Printf("%s — %s subtitles written (%s).\n", tlabel, target, fmtDuration(time.Since(started)))
		t.extraSRT = append(t.extraSRT, path)
		subs = append(subs, subInput{path: path, lang: target, def: len(subs) == 0})
	}
	return subs
}
