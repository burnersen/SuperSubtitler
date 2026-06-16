//go:build windows && amd64

package main

import (
	"bufio"
	"fmt"
	"os"
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
// (always performed) detection, prompting the user only when nothing is
// conclusive. forcedLang short-circuits detection; -translate skips the prompt.
func decideLanguage(cfg *AppConfig, tagLang, detectedLang string) string {
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
		return resolveLangDiscrepancy(tag, det)
	case det != "":
		return det
	case tag != "":
		return tag
	case cfg.translate:
		return "" // translate can auto-detect the source; no need to ask
	default:
		return promptForLanguage()
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
func resolveLangDiscrepancy(tag, det string) string {
	fmt.Println()
	pWarn.Printf("Language mismatch: container tag says %s, detection says %s.\n",
		pterm.LightYellow(tag), pterm.LightCyan(det))
	pterm.Println(pterm.Gray(fmt.Sprintf(
		"  Press Enter to use the detected %s, type %s for the tag, or enter another code.",
		pterm.LightCyan(det), pterm.LightYellow(tag))))
	pterm.Print(pterm.LightCyan("  Language: "))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if code := normalizeLang(line); code != "" {
		return code
	}
	return det
}

// promptForLanguage asks the user for a language code when detection and tags
// both fail. Runs interactively on the console.
func promptForLanguage() string {
	fmt.Println()
	pWarn.Println("Could not determine the spoken language for this file.")
	pterm.Println(pterm.Gray("  Enter the language code (e.g. de, en, es, fr) and press Enter."))
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(pterm.LightCyan("  Language code: "))
		line, _ := reader.ReadString('\n')
		code := normalizeLang(line)
		if code != "" {
			return code
		}
		pWarn.Println("Please enter a non-empty language code.")
	}
}
