//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"
)

// normalizeLang maps assorted language spellings/codes to a 2-letter code.
func normalizeLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "-_"); i > 0 { // "en-US" -> "en"
		s = s[:i]
	}
	switch s {
	case "de", "deu", "ger", "german", "deutsch":
		return "de"
	case "en", "eng", "english":
		return "en"
	}
	// 3-letter -> common 2-letter, otherwise first two letters as a best guess.
	alias := map[string]string{
		"spa": "es", "fra": "fr", "fre": "fr", "ita": "it", "por": "pt",
		"nld": "nl", "dut": "nl", "rus": "ru", "jpn": "ja", "kor": "ko",
		"zho": "zh", "chi": "zh", "pol": "pl", "swe": "sv", "dan": "da",
		"nor": "no", "fin": "fi", "ces": "cs", "cze": "cs", "tur": "tr",
		"ara": "ar", "ell": "el", "gre": "el", "hun": "hu", "ron": "ro",
	}
	if v, ok := alias[s]; ok {
		return v
	}
	if len(s) >= 2 {
		return s[:2]
	}
	return s
}

// langPlan is the resolved decision for one file.
type langPlan struct {
	model   string
	task    string // "transcribe" | "translate"
	srcLang string // passed to the engine; "" = let the engine autodetect
	outLang string // language tag written into the MKV + sidecar filename
}

// decideLanguage resolves the source language from the container tag and the
// detection, prompting the user only when nothing is conclusive. forcedLang
// short-circuits detection; -translate skips the prompt; -auto answers every
// prompt with its default (detected language, or engine guess when unknown).
// file names the input in the question event.
func decideLanguage(cfg *AppConfig, tagLang, detectedLang, file string) string {
	if cfg.forcedLang != "" {
		return normalizeLang(cfg.forcedLang)
	}
	tag := normalizeLang(tagLang)
	det := normalizeLang(detectedLang)

	switch {
	case det != "" && tag != "" && det != tag:
		if cfg.translate {
			return det // output is English anyway; no need to ask
		}
		if cfg.auto {
			pInfo.Printf("Language mismatch (tag %s, detected %s) — -auto keeps the detected language.\n", tag, det)
			return det
		}
		return resolveLangDiscrepancy(tag, det, file)
	case det != "":
		return det
	case tag != "":
		return tag
	case cfg.translate:
		return "" // translate can auto-detect the source; no need to ask
	case cfg.auto:
		pWarn.Println("Could not determine the spoken language — -auto lets the engine guess (tagged und).")
		return ""
	default:
		return promptForLanguage(file)
	}
}

// buildPlan turns the resolved source language into the concrete engine plan.
// Default: transcribe in the source's OWN language with the fast multilingual
// model. With -translate: translate to English (the only target Whisper supports)
// with the large model — turbo cannot translate. The detected source language is
// still passed to the translate task when known, as it improves the result.
func buildPlan(cfg *AppConfig, srcLang string) langPlan {
	if cfg.translate {
		return langPlan{model: appSettings.modelTranslate, task: "translate", srcLang: srcLang, outLang: "en"}
	}
	outLang := srcLang
	if outLang == "" {
		outLang = "und" // source unknown; tag the track as undetermined
	}
	return langPlan{model: appSettings.modelTranscribe, task: "transcribe", srcLang: srcLang, outLang: outLang}
}

// resolveLangDiscrepancy is called only when the container tag and the detected
// language disagree. It asks which to use; pressing Enter keeps the detected
// language (the safer default — container tags are often wrong). Typing the tag
// code uses the tag; any other code overrides both.
func resolveLangDiscrepancy(tag, det, file string) string {
	fmt.Println()
	pWarn.Printf("Language mismatch: container tag says %s, detection says %s.\n",
		pterm.LightYellow(tag), pterm.LightCyan(det))
	pterm.Println(pterm.Gray(fmt.Sprintf(
		"  Press Enter to use the detected %s, type %s for the tag, or enter another code.",
		pterm.LightCyan(det), pterm.LightYellow(tag))))
	pterm.Print(pterm.LightCyan("  Language: "))
	emitQuestion(eventQuestion{
		Kind: questionLangMismatch, File: file, Tag: tag, Detected: det, Default: det,
		Hint: "Enter = detected language",
	})
	line, _ := readAnswerLine()
	if code := normalizeLang(line); code != "" {
		return code
	}
	return det
}

// promptForLanguage asks the user for a language code when detection and tags
// both fail. An empty answer or a closed stdin lets the engine guess (""), so a
// front-end that goes away can never leave this loop spinning.
func promptForLanguage(file string) string {
	fmt.Println()
	pWarn.Println("Could not determine the spoken language for this file.")
	pterm.Println(pterm.Gray("  Enter the language code (e.g. de, en, es, fr), or just Enter to let the engine guess."))
	fmt.Print(pterm.LightCyan("  Language code: "))
	emitQuestion(eventQuestion{Kind: questionLangUnknown, File: file, Default: "und", Hint: "Enter = let the engine guess"})
	line, _ := readAnswerLine()
	return normalizeLang(line)
}
