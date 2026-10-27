// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"
	"strings"
)

// The Settings page is built from SuperSubtitler_Config.ini itself: every
// section and key with the comment text the CLI wrote above it. Nothing is
// re-described here, so the page can never drift from the file it edits.

// IniKey is one "key=value" line and the comment lines directly above it.
type IniKey struct {
	Key     string  `json:"key"`
	Value   string  `json:"value"`
	Help    string  `json:"help"` // the comment above the key in the file
	Doc     *KeyDoc `json:"doc"`  // plain-words description, nil if unknown
	Default string  `json:"default"`
}

// IniSection is one [section] with its leading comment block.
type IniSection struct {
	Name string   `json:"name"`
	Help string   `json:"help"`
	Keys []IniKey `json:"keys"`
}

// ParseIni reads the INI text into sections. Keys before any [section] land in
// a section with an empty name. Comment lines ("#" or ";") directly above a
// key become its help; the comment block right under a section header (before
// its first key) becomes the section's help.
func ParseIni(text string) []IniSection {
	var sections []IniSection
	cur := IniSection{}
	var comments []string
	started := false // a section exists that keys may join

	flushComments := func() string {
		h := strings.Join(comments, "\n")
		comments = nil
		return h
	}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			// A blank line ends a comment block that belongs to nobody yet,
			// except right under a section header, where it is the section help.
			if started && len(cur.Keys) == 0 && cur.Help == "" && len(comments) > 0 {
				cur.Help = flushComments()
			}
		case strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
			comments = append(comments, strings.TrimSpace(strings.TrimLeft(line, "#;")))
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			if started || len(cur.Keys) > 0 {
				sections = append(sections, cur)
			}
			comments = nil // comments above a header are the file's preamble
			cur = IniSection{Name: strings.TrimSpace(line[1 : len(line)-1])}
			started = true
		default:
			key, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			help := flushComments()
			if len(cur.Keys) == 0 && cur.Help == "" && help != "" {
				// The block under the header describes the whole section.
				cur.Help, help = help, ""
			}
			cur.Keys = append(cur.Keys, IniKey{Key: key, Value: strings.TrimSpace(val), Help: help, Doc: DocFor(key)})
		}
	}
	if started || len(cur.Keys) > 0 {
		sections = append(sections, cur)
	}
	return sections
}

// IniValues returns key -> value of an INI text (used for the CLI's defaults).
func IniValues(text string) map[string]string {
	out := map[string]string{}
	for _, sec := range ParseIni(text) {
		for _, k := range sec.Keys {
			out[k.Key] = k.Value
		}
	}
	return out
}

// WithDefaults fills in each key's default value (from IniValues of the
// CLI's -printdefaults output).
func WithDefaults(sections []IniSection, defaults map[string]string) []IniSection {
	for i := range sections {
		for j := range sections[i].Keys {
			sections[i].Keys[j].Default = defaults[sections[i].Keys[j].Key]
		}
	}
	return sections
}

// ApplyIniValues writes new values into the INI text in place: comments, order
// and layout stay exactly as they are; only the text after "=" changes. Keys
// that do not exist in the file are reported as an error rather than appended
// somewhere surprising.
func ApplyIniValues(text string, values map[string]string) (string, error) {
	for k, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("the value of %s must be a single line", k)
		}
	}
	crlf := strings.Contains(text, "\r\n")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	done := map[string]bool{}
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "[") {
			continue
		}
		key, _, ok := strings.Cut(raw, "=")
		if !ok {
			continue
		}
		name := strings.TrimSpace(key)
		if v, want := values[name]; want {
			lines[i] = key + "=" + strings.TrimSpace(v)
			done[name] = true
		}
	}
	for k := range values {
		if !done[k] {
			return "", fmt.Errorf("%s is not in the settings file", k)
		}
	}
	out := strings.Join(lines, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, nil
}
