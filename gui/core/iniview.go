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
	Key   string `json:"key"`
	Value string `json:"value"`
	Help  string `json:"help"`
	Hint  string `json:"hint"` // allowed values, when known
}

// IniSection is one [section] with its leading comment block.
type IniSection struct {
	Name string   `json:"name"`
	Help string   `json:"help"`
	Keys []IniKey `json:"keys"`
}

// keyHints mirror the CLI's validation (Config.go settingsProblem). They are
// only shown as help; the CLI's own -checkconfig decides what is saved.
var keyHints = map[string]string{
	"detect_seconds":             "30 – 1800",
	"detect_start_percent":       "0 – 90",
	"device":                     "cuda | cpu",
	"beam_size":                  "1 – 10",
	"vad_filter":                 "true | false",
	"vad_threshold":              "0 – 1",
	"install_cuda":               "true | false",
	"condition_on_previous_text": "true | false",
	"leadin_ms":                  "0 – 5000",
	"surround_mode":              "center_emphasis | pure_center",
	"sub_max_chars_per_line":     "20 – 80",
	"sub_max_lines":              "1 – 3",
	"sub_max_cps":                "5 – 40",
	"sub_min_dur_ms":             "200 – sub_max_dur_ms",
	"sub_max_dur_ms":             "1000 – 20000",
	"python_version":             "X.Y.Z with a python.org embed build",
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
			cur.Keys = append(cur.Keys, IniKey{Key: key, Value: strings.TrimSpace(val), Help: help, Hint: keyHints[key]})
		}
	}
	if started || len(cur.Keys) > 0 {
		sections = append(sections, cur)
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
