// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package core

// The Settings page explains every key in plain words: what it does, what is
// a good value, and which values SuperSubtitler accepts at all. Hard limits
// (Min/Max) mirror the CLI's own check (Config.go settingsProblem), which
// still has the last word via -checkconfig; the recommended range is advice —
// outside it the page warns, but lets the user save.

// Choice is one entry of a drop-down.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// KeyDoc describes one setting for the Settings page.
type KeyDoc struct {
	Title  string `json:"title"`
	Help   string `json:"help"`
	Advice string `json:"advice"` // the recommendation in words
	// Kind: int | float | bool | choice | text
	Kind    string   `json:"kind"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	RecMin  *float64 `json:"recMin,omitempty"`
	RecMax  *float64 `json:"recMax,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	// Open: the drop-down also accepts a value typed by hand (model names).
	Open bool `json:"open,omitempty"`
	// Expert: changing it removes the tested setup; the page says so loudly.
	Expert bool `json:"expert,omitempty"`
}

func f(v float64) *float64 { return &v }

var modelChoices = []Choice{
	{"large-v3-turbo", "large-v3-turbo — fast and accurate (recommended, tested)"},
	{"large-v3", "large-v3 — most accurate, about half as fast (tested)"},
	{"medium", "medium — faster, noticeably less accurate"},
	{"small", "small — much faster, for weak hardware"},
	{"base", "base — very fast, rough"},
	{"tiny", "tiny — fastest, only for tests"},
}

// keyDocs is keyed by the INI key.
var keyDocs = map[string]KeyDoc{
	// [detection]
	"detect_seconds": {
		Title:  "Sample length for language detection (seconds)",
		Help:   "How much of each file is listened to before deciding which language is spoken. The sample is split into 30-second windows that vote.",
		Advice: "120–300. Longer is more reliable for quiet starts or mixed languages, but every file takes longer before it begins.",
		Kind:   "int", Min: f(30), Max: f(1800), RecMin: f(120), RecMax: f(300),
	},
	"detect_start_percent": {
		Title:  "Where the sample starts (% of the file)",
		Help:   "Skips the beginning so the language is not judged on an intro, logo music or silence.",
		Advice: "5–20. 0 starts at the very beginning; a high value can miss short files' speech.",
		Kind:   "int", Min: f(0), Max: f(90), RecMin: f(5), RecMax: f(20),
	},
	"detect_model": {
		Title:  "Model for language detection",
		Help:   "Only used to recognise the spoken language, not to write subtitles.",
		Advice: "large-v3-turbo. Smaller models start faster but misjudge the language more often.",
		Kind:   "choice", Choices: modelChoices, Open: true,
	},
	// [models]
	"model_transcribe": {
		Title:  "Model for subtitles",
		Help:   "The speech model that writes the subtitles in the spoken language. Bigger models make fewer mistakes but need more time and video memory.",
		Advice: "large-v3-turbo. large-v3 if accuracy matters more than speed. Only the two large models are pinned and verified.",
		Kind:   "choice", Choices: modelChoices, Open: true,
	},
	"model_translate": {
		Title:  "Model for “Translate to English”",
		Help:   "Used only when translating. It must be a large model that is not “turbo” — turbo cannot translate.",
		Advice: "large-v3 (needs about 6 GB of video memory; use “Less video memory” on smaller cards).",
		Kind:   "choice", Choices: []Choice{
			{"large-v3", "large-v3 — the translation model (recommended, tested)"},
			{"medium", "medium — less video memory, weaker translation"},
		}, Open: true,
	},
	// [engine]
	"device": {
		Title:  "Where the engine runs",
		Help:   "cuda uses the NVIDIA graphics card and falls back to the processor by itself if that fails. cpu always uses the processor.",
		Advice: "cuda. Choose cpu only if there is no NVIDIA card — it is many times slower.",
		Kind:   "choice", Choices: []Choice{{"cuda", "cuda — NVIDIA graphics card (recommended)"}, {"cpu", "cpu — processor only (slow)"}},
	},
	"compute_type": {
		Title:  "Number format on the graphics card",
		Help:   "How precisely the model computes. float16 is what modern NVIDIA cards are built for.",
		Advice: "float16. int8_float16 needs less video memory with almost the same quality. float32 only for very old cards.",
		Kind:   "choice", Choices: []Choice{
			{"float16", "float16 — recommended"},
			{"int8_float16", "int8_float16 — less video memory"},
			{"int8", "int8 — least memory, a bit less accurate"},
			{"float32", "float32 — old cards, slow"},
		},
	},
	"compute_type_lowmem": {
		Title:  "Number format with “Less video memory”",
		Help:   "Used instead of the one above when “Less video memory” is ticked.",
		Advice: "int8 (or int8_float16 for slightly better quality).",
		Kind:   "choice", Choices: []Choice{
			{"int8", "int8 — least memory (recommended)"},
			{"int8_float16", "int8_float16 — little memory, slightly better"},
			{"float16", "float16 — no saving"},
		},
	},
	"beam_size": {
		Title:  "Search width (beam size)",
		Help:   "How many candidate sentences the model weighs at once. Higher can be slightly more accurate, but is slower.",
		Advice: "5. 1 is fastest; above 5 hardly helps.",
		Kind:   "int", Min: f(1), Max: f(10), RecMin: f(3), RecMax: f(5),
	},
	// [python]
	"python_version": {
		Title:  "Python version (expert)",
		Help:   "The embedded Python the engine runs in. The shipped value selects SuperSubtitler's tested set of exact versions, each checked.",
		Advice: "Leave unchanged. Any other version installs the newest packages instead — untested, and it can stop working.",
		Kind:   "text", Expert: true,
	},
	"engine_spec": {
		Title:  "Engine package (expert)",
		Help:   "“faster-whisper” means: the tested, pinned set. Anything else is installed as written, newest versions, unchecked.",
		Advice: "faster-whisper (leave unchanged).",
		Kind:   "text", Expert: true,
	},
	"install_cuda": {
		Title:  "Install the CUDA libraries",
		Help:   "Downloads NVIDIA's CUDA libraries (about 1.3 GB) so the graphics card works without a system-wide CUDA installation.",
		Advice: "true. false only on machines without an NVIDIA card, to save the download.",
		Kind:   "bool",
	},
	// [vad]
	"vad_filter": {
		Title:  "Silence filter",
		Help:   "Only speech is transcribed; pauses stay without subtitles. Without it the model also “hears” words in silence and music (hallucinations).",
		Advice: "true. The “Silence filter” switch on the Subtitle page turns it off for a single run if needed.",
		Kind:   "bool",
	},
	"vad_threshold": {
		Title:  "Silence filter sensitivity",
		Help:   "From 0 to 1: how sure the filter must be that someone speaks. Lower keeps quiet or distant speech, higher removes more noise and music.",
		Advice: "0.35–0.60. Lower it if quiet sentences are missing, raise it if music or noise turns into text.",
		Kind:   "float", Min: f(0), Max: f(1), RecMin: f(0.35), RecMax: f(0.6),
	},
	"vad_speech_pad_ms": {
		Title:  "Cushion around speech (ms)",
		Help:   "Extra time kept before and after each detected bit of speech, so first and last syllables are not cut off.",
		Advice: "200–600. Too low cuts word beginnings; too high lets pauses in again.",
		Kind:   "int", Min: f(0), Max: f(2000), RecMin: f(200), RecMax: f(600),
	},
	// [robustness]
	"condition_on_previous_text": {
		Title:  "Use the previous sentence as context",
		Help:   "Lets the model see what it wrote before. Can help consistency, but it is the main cause of endless repetition loops.",
		Advice: "false.",
		Kind:   "bool",
	},
	"initial_prompt": {
		Title:  "Hint text (optional)",
		Help:   "A short text in the spoken language that steers spelling and style — e.g. names, places or technical terms that appear in the film.",
		Advice: "Empty, or one short sentence with the names that matter. Long texts can make results worse.",
		Kind:   "text",
	},
	// [audio]
	"leadin_ms": {
		Title:  "Run-up silence (ms)",
		Help:   "Silence put in front of the audio so the model does not swallow the very first word. It is subtracted again, the timing stays exact.",
		Advice: "100–500.",
		Kind:   "int", Min: f(0), Max: f(5000), RecMin: f(100), RecMax: f(500),
	},
	"surround_mode": {
		Title:  "5.1/7.1 sound: which channels",
		Help:   "In surround sound the dialogue sits mostly on the centre channel. center_emphasis mixes the centre loud with a little of the front channels; pure_center uses the centre only.",
		Advice: "center_emphasis. pure_center if music or effects drown the voices.",
		Kind:   "choice", Choices: []Choice{
			{"center_emphasis", "center_emphasis — centre loud plus front (recommended)"},
			{"pure_center", "pure_center — centre channel only"},
		},
	},
	// [subtitles]
	"sub_max_chars_per_line": {
		Title:  "Characters per line (max.)",
		Help:   "How long one subtitle line may get before it wraps.",
		Advice: "32–42 (Netflix uses 42). More is hard to read in time.",
		Kind:   "int", Min: f(20), Max: f(80), RecMin: f(32), RecMax: f(42),
	},
	"sub_max_lines": {
		Title:  "Lines per subtitle (max.)",
		Help:   "How many lines one subtitle may have.",
		Advice: "2 — the standard. 3 covers too much of the picture.",
		Kind:   "int", Min: f(1), Max: f(3), RecMin: f(2), RecMax: f(2),
	},
	"sub_max_cps": {
		Title:  "Reading speed (characters per second, max.)",
		Help:   "If a subtitle would have to be read faster than this, it stays on screen a little longer (as far as the next one allows).",
		Advice: "15–20 (17 is the usual standard).",
		Kind:   "float", Min: f(5), Max: f(40), RecMin: f(15), RecMax: f(20),
	},
	"sub_min_dur_ms": {
		Title:  "Shortest display time (ms)",
		Help:   "No subtitle disappears faster than this, so even a single word can be read.",
		Advice: "700–1000 (833 = 5/6 s, a common standard). Must not be above the longest display time.",
		Kind:   "int", Min: f(200), Max: f(20000), RecMin: f(700), RecMax: f(1000),
	},
	"sub_max_dur_ms": {
		Title:  "Longest display time (ms)",
		Help:   "A subtitle is split if it would stay on screen longer than this.",
		Advice: "5000–7000.",
		Kind:   "int", Min: f(1000), Max: f(20000), RecMin: f(5000), RecMax: f(7000),
	},
	"sub_min_gap_ms": {
		Title:  "Gap between two subtitles (ms)",
		Help:   "A short blank between subtitles so the eye notices the change (anti-flicker).",
		Advice: "80–125 (two to three frames).",
		Kind:   "int", Min: f(0), Max: f(1000), RecMin: f(80), RecMax: f(125),
	},
	"sub_gap_split_ms": {
		Title:  "Pause that starts a new subtitle (ms)",
		Help:   "When the speaker pauses at least this long, the next words get a new subtitle instead of being added to the current one.",
		Advice: "300–800. Lower gives more, shorter subtitles; higher joins sentences across pauses.",
		Kind:   "int", Min: f(100), Max: f(5000), RecMin: f(300), RecMax: f(800),
	},
	// [speakers]
	"speaker_change_threshold": {
		Title:  "How sure a speaker change must be",
		Help:   "Only used with “Mark speaker changes”. Every moment is looked at about nine times; this is the share of those looks that must find a change of voice there. Lower finds more changes, including false ones (a new subtitle or “- ” lines where only one person speaks); higher keeps only the sure ones (quick replies may be missed).",
		Advice: "0.15–0.3 (0.15 is the measured best). Raise it if “- ” lines appear where one person speaks; lower it if quick replies are missed.",
		Kind:   "float", Min: f(0.05), Max: f(0.9), RecMin: f(0.15), RecMax: f(0.3),
	},
}

// DocFor returns the description of a key (nil for unknown keys).
func DocFor(key string) *KeyDoc {
	if d, ok := keyDocs[key]; ok {
		return &d
	}
	return nil
}
