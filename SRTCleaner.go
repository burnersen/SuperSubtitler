//go:build windows && amd64

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const cleanerConfigFileName = "SuperSubtitler_SRTCleaner.txt"

// cleanerRules holds lowercased match terms. substr matches anywhere in a cue's
// text; exact matches only when the whole cue text equals the term.
type cleanerRules struct {
	substr []string
	exact  []string
}

// activeCleaner is loaded once at startup and applied to every generated SRT.
var activeCleaner cleanerRules

func cleanerConfigPath() string {
	return filepath.Join(exeDir(), cleanerConfigFileName)
}

// loadCleanerConfig reads the cleaner term list, creating it with defaults on
// first run. Parsing: '#'/';' comment lines and blanks are ignored; a leading
// '=' marks an exact-match term, everything else is a substring term. All terms
// are compared case-insensitively.
func loadCleanerConfig() cleanerRules {
	path := cleanerConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		writeDefaultCleanerConfig(path)
		data = []byte(defaultCleanerConfig)
	}
	return parseCleanerRules(string(data))
}

func parseCleanerRules(text string) cleanerRules {
	r := cleanerRules{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "=") {
			if term := strings.ToLower(strings.TrimSpace(line[1:])); term != "" {
				r.exact = append(r.exact, term)
			}
			continue
		}
		r.substr = append(r.substr, strings.ToLower(line))
	}
	return r
}

func writeDefaultCleanerConfig(path string) {
	if err := os.WriteFile(path, []byte(defaultCleanerConfig), 0644); err != nil {
		pWarn.Printf("Could not write %s: %v\n", cleanerConfigFileName, err)
	}
}

// cleanSRT removes every cue whose text matches a rule and renumbers the rest.
// Unmatched cues keep their timing and text verbatim.
func cleanSRT(srt string, r cleanerRules) string {
	if strings.TrimSpace(srt) == "" {
		return srt
	}
	norm := strings.ReplaceAll(srt, "\r\n", "\n")
	blocks := strings.Split(norm, "\n\n")

	var b strings.Builder
	idx := 1
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		timingAt := -1
		for i, l := range lines {
			if strings.Contains(l, "-->") {
				timingAt = i
				break
			}
		}
		if timingAt < 0 {
			continue // not a valid cue block (no timing line)
		}
		timing := strings.TrimSpace(lines[timingAt])
		textLines := lines[timingAt+1:]
		if cueMatches(textLines, r) {
			continue
		}
		fmt.Fprintf(&b, "%d\n%s\n%s\n\n", idx, timing, strings.Join(textLines, "\n"))
		idx++
	}
	return b.String()
}

func cueMatches(textLines []string, r cleanerRules) bool {
	joined := strings.ToLower(strings.TrimSpace(strings.Join(textLines, " ")))
	if joined == "" {
		return false
	}
	for _, e := range r.exact {
		if joined == e {
			return true
		}
	}
	for _, s := range r.substr {
		if s != "" && strings.Contains(joined, s) {
			return true
		}
	}
	return false
}

const defaultCleanerConfig = `# SuperSubtitler — SRT cleaner
# ----------------------------
# One term per line. Matching is case-insensitive. Delete this file to
# regenerate these defaults.
#
# STANDARD MODE (substring): a line without a prefix deletes any cue whose text
# contains the term ANYWHERE. Example: "2017" also removes "Im Jahr 2017".
#
# EXACT MODE (strict): a line starting with "=" deletes a cue ONLY when its
# whole text equals the term exactly (incl. punctuation). Example: "=2017"
# removes a cue that is only "2017", but keeps "Im Jahr 2017".
#
# Nothing is hard-coded: every term below is just a starting suggestion. Edit or
# delete any line — the program only removes what this file lists.

# --- Standard matches (substring) ---
untertitel
funk, 2017
uebersetzung von
übersetzung
whisper
©
copyright
produktion:
produziert von
[musik]

# --- Exact matches (strict) ---
=2016
=2017
=2018
=2019
=2020
=2021
=2022
=2023
=2024
=2025
=2026
=ende.
`
