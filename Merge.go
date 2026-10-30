//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pterm/pterm"
)

// subtitleExtensions are sidecar subtitle files accepted ONLY in merge mode
// (they are not transcribable inputs). Text formats become SRT-friendly tracks,
// bitmap formats (sup/idx/sub) ride along via stream copy.
var subtitleExtensions = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".vtt": true,
	".sup": true, ".idx": true, ".sub": true,
}

// knownLangCodes is the allow-list used when guessing a language from a sidecar
// file name (e.g. "Movie.de.srt" -> de). Restricting to real codes avoids
// mistaking tokens like "hd" or "joined" for a language.
var knownLangCodes = map[string]bool{
	"de": true, "en": true, "es": true, "fr": true, "it": true, "pt": true,
	"nl": true, "ru": true, "pl": true, "tr": true, "ja": true, "ko": true,
	"zh": true, "ar": true, "el": true, "hu": true, "ro": true, "cs": true,
	"sv": true, "da": true, "no": true, "fi": true, "uk": true, "he": true,
	"hi": true, "th": true, "vi": true, "id": true, "ca": true, "sk": true,
	"bg": true, "hr": true,
}

// fileLangFromName extracts a language code from the token before the extension
// ("Movie.de.srt" -> "de", "audio.eng.m4a" -> "en"), but only when it maps to a
// real, known language code. Returns "" otherwise.
func fileLangFromName(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	code := normalizeLang(name[i+1:])
	if knownLangCodes[code] {
		return code
	}
	return ""
}

// classifyMergeInputs reports whether the dropped set looks like a merge job:
// exactly one video plus one or more audio/subtitle files, and no folders or
// unknown files. Returns the resolved (long-path) members.
func classifyMergeInputs(args []string) (video string, audios, subs []string, ok bool) {
	videos := 0
	for _, a := range args {
		info, err := os.Stat(a)
		if err != nil || info.IsDir() {
			return "", nil, nil, false
		}
		ext := strings.ToLower(filepath.Ext(a))
		switch {
		case videoExtensions[ext]:
			videos++
			video = getLongPathName(a)
		case audioExtensions[ext]:
			audios = append(audios, getLongPathName(a))
		case subtitleExtensions[ext]:
			subs = append(subs, getLongPathName(a))
		default:
			return "", nil, nil, false // unknown file -> not a clean merge set
		}
	}
	if videos != 1 || len(audios)+len(subs) == 0 {
		return "", nil, nil, false
	}
	return video, audios, subs, true
}

// askMerge shows the detected merge set and asks for confirmation (default yes,
// which -auto and a closed stdin also take).
func askMerge(video string, audios, subs []string, auto bool, container string) bool {
	fmt.Println()
	pInfo.Printf("This looks like a merge: 1 video + %d audio + %d subtitle file(s).\n",
		len(audios), len(subs))
	pterm.Println(pterm.Gray("  Video : " + baseName(video)))
	for _, a := range audios {
		pterm.Println(pterm.Gray("  Audio : " + baseName(a)))
	}
	for _, s := range subs {
		pterm.Println(pterm.Gray("  Sub   : " + baseName(s)))
	}
	if auto {
		pterm.Println(pterm.Gray("  -auto: merging."))
		return true
	}
	pterm.Print(pterm.LightCyan("  Merge them into one .joined." + container + "? [Y/n]: "))
	emitQuestion(eventQuestion{Kind: questionMerge, File: video, Default: "y", Hint: "Enter = yes"})
	line, _ := readAnswerLine()
	return parseYes(line, true)
}

// parseYes reads a yes/no answer (English or German); empty returns def.
func parseYes(line string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes", "j", "ja":
		return true
	}
	return false
}

// runMergeMode losslessly muxes the dropped files into "<video>.joined.mkv"
// (with -mp4 ".joined.mp4"). Picture always comes from the base video. Audio:
// the dropped audio files if any, otherwise the base video's own audio (never
// silent). Subtitles: the dropped subtitle files if any, otherwise the base
// video's own. Everything is stream-copied (no re-encode; in an MP4 text
// subtitles become mov_text). Dropped tracks are language-tagged from their
// file names when possible. Reports whether the merge succeeded.
func runMergeMode(ctx context.Context, cfg *AppConfig, video string, audios, subs []string) bool {
	base := outputBase(cfg, video)
	emitEvent(eventFile{Ev: "file", Index: 1, Total: 1, Name: baseName(video), Path: video})
	emitStage(1, 0, "mux")
	res := eventResult{Ev: "result", Index: 1, Name: baseName(video), Status: "failed"}

	container, note := outputContainer(cfg), ""
	if container == containerMP4 {
		// A file that cannot be probed is left to the MP4 attempt and its
		// MKV fallback.
		if streams, err := mergeStreams(ctx, video, audios, subs); err == nil {
			if why := mp4Problems(streams); len(why) > 0 {
				note, container = mkvInsteadNote(why), containerMKV
				pWarn.Println(note + ".")
			}
		}
	}
	out := uniqueJoinedPath(base, container)

	spinner, _ := pterm.DefaultSpinner.WithText("Merging into " + baseName(out) + " ...").Start()
	_, err := runProcess(ctx, ffmpegPath, mergeArgs(video, audios, subs, container, out)...)
	if err != nil && container == containerMP4 && ctx.Err() == nil {
		// Whatever FFmpeg still refuses in an MP4 goes into an MKV instead.
		_ = os.Remove(out)
		note = "MKV instead of MP4: FFmpeg could not write this file as MP4 (" + shortErr(err) + ")"
		_ = spinner.Stop()
		pWarn.Println(note + ".")
		container = containerMKV
		out = uniqueJoinedPath(base, container)
		spinner, _ = pterm.DefaultSpinner.WithText("Merging into " + baseName(out) + " ...").Start()
		_, err = runProcess(ctx, ffmpegPath, mergeArgs(video, audios, subs, container, out)...)
	}
	if err != nil {
		_ = spinner.Stop()
		_ = os.Remove(out)
		if ctx.Err() != nil {
			pAbort.Println("Merge cancelled.")
			res.Message = "cancelled"
			emitEvent(res)
			return false
		}
		pErr.Println("Merge failed: " + shortErr(err))
		res.Message = shortErr(err)
		emitEvent(res)
		return false
	}
	_ = spinner.Stop()

	if err := copyTimestamps(video, out); err != nil {
		pWarn.Println("Could not copy timestamps to output: " + shortErr(err))
	}
	if len(audios) == 0 {
		pInfo.Println("No audio file given — kept the video's own audio.")
	}
	pOK.Println("Merged → " + baseName(out))
	res.Status, res.Outputs, res.Message = "success", []string{out}, note
	emitEvent(res)
	return true
}

// mergeArgs is the FFmpeg command line of a merge into out.
func mergeArgs(video string, audios, subs []string, container, out string) []string {
	args := []string{"-y", "-v", "error", "-i", video}
	for _, a := range audios {
		args = append(args, "-i", a)
	}
	for _, s := range subs {
		args = append(args, "-i", s)
	}

	args = append(args, "-map", "0:v:0")
	if len(audios) > 0 {
		for i := range audios {
			args = append(args, "-map", fmt.Sprintf("%d:a?", 1+i))
		}
	} else {
		args = append(args, "-map", "0:a?")
	}
	subStart := 1 + len(audios)
	if len(subs) > 0 {
		for i := range subs {
			args = append(args, "-map", fmt.Sprintf("%d:s?", subStart+i))
		}
	} else {
		args = append(args, "-map", "0:s?")
	}

	args = append(args, "-c", "copy")
	if container == containerMP4 {
		args = append(args, "-c:s", "mov_text")
	}

	// Language tags from the dropped file names (best-effort; assumes one stream
	// per sidecar, which holds for typical audio/subtitle sidecars).
	if len(audios) > 0 {
		for i, a := range audios {
			if l := fileLangFromName(a); l != "" {
				args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), "language="+trackLang(container, l))
			}
		}
	}
	if len(subs) > 0 {
		for i, s := range subs {
			if l := fileLangFromName(s); l != "" {
				args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "language="+trackLang(container, l))
			}
		}
		args = append(args, "-disposition:s:0", "default")
	}
	return append(args, out)
}

// uniqueJoinedPath returns "<base>.joined.<container>", numbering it if that
// exists.
func uniqueJoinedPath(base, container string) string {
	p := base + ".joined." + container
	for n := 2; fileExists(p); n++ {
		p = fmt.Sprintf("%s.joined.%d.%s", base, n, container)
	}
	return p
}
