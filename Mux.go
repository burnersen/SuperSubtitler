//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// subInput is one SRT sidecar plus the language tag it should carry in the video file.
type subInput struct {
	path string
	lang string
	def  bool // the default subtitle track (a translation the user asked for)
}

// buildSubbed soft-muxes one or more SRT sidecars into srcVideo, each as a
// language-tagged subtitle track (the first is the default), and writes
// "<base>.subbed.mkv" or, with -mp4, "<base>.subbed.mp4". In full mode every
// source stream is copied losslessly; in -lean mode only the video, the
// subtitled audio tracks (audioOrdinals) and the new subtitles survive.
// note is set when MP4 was asked for but the file had to become an MKV: it
// says why.
func buildSubbed(ctx context.Context, cfg *AppConfig, srcVideo string, subs []subInput, audioOrdinals []int, base string) (out, note string, err error) {
	container := outputContainer(cfg)
	pd, perr := probeFile(ctx, srcVideo)
	// A source that cannot be probed is left to the MP4 attempt below and
	// its MKV fallback.
	if container == containerMP4 && perr == nil {
		if why := mp4Problems(keptStreams(pd, cfg.lean, audioOrdinals)); len(why) > 0 {
			note, container = mkvInsteadNote(why), containerMKV
		}
	}
	// Without -lean the source's own subtitle tracks come first: ours are
	// numbered after them.
	first := 0
	if !cfg.lean && perr == nil {
		first = pd.subtitleCount()
	}

	out = base + ".subbed." + container
	err = muxSubbed(ctx, cfg, container, srcVideo, subs, audioOrdinals, first, out)
	if err != nil && container == containerMP4 && ctx.Err() == nil {
		// The checks above know the usual suspects; whatever FFmpeg still
		// refuses in an MP4 goes into an MKV instead of failing the file.
		_ = os.Remove(out)
		note = "MKV instead of MP4: FFmpeg could not write this file as MP4 (" + shortErr(err) + ")"
		container, out = containerMKV, base+".subbed.mkv"
		err = muxSubbed(ctx, cfg, container, srcVideo, subs, audioOrdinals, first, out)
	}
	if err != nil {
		return "", "", err
	}
	if err := copyTimestamps(srcVideo, out); err != nil {
		// Non-fatal: the file is valid, only its file dates differ.
		pWarn.Println("Could not copy timestamps to output: " + shortErr(err))
	}
	if err := validateOutput(ctx, out); err != nil {
		return "", "", err
	}
	return out, note, nil
}

// muxSubbed runs the one FFmpeg call of buildSubbed for the given container.
// first is the number of the source's own subtitle tracks kept ahead of ours.
func muxSubbed(ctx context.Context, cfg *AppConfig, container, srcVideo string, subs []subInput, audioOrdinals []int, first int, outPath string) error {
	args := []string{"-y", "-v", "error", "-i", srcVideo}
	for _, s := range subs {
		args = append(args, "-i", s.path)
	}
	if cfg.lean {
		args = append(args, "-map", "0:v:0")
		for _, ord := range audioOrdinals {
			args = append(args, "-map", fmt.Sprintf("0:a:%d", ord))
		}
	} else {
		args = append(args, "-map", "0")
	}
	for i := range subs {
		args = append(args, "-map", fmt.Sprintf("%d:0", i+1))
	}
	// MP4 holds text subtitles only as mov_text (the source's own text
	// subtitles are converted alike; nothing of them is lost).
	subCodec := "srt"
	if container == containerMP4 {
		subCodec = "mov_text"
	}
	args = append(args, "-c", "copy", "-c:s", subCodec)
	def := first
	for i, s := range subs {
		args = append(args, fmt.Sprintf("-metadata:s:s:%d", first+i), "language="+trackLang(container, s.lang))
		if s.def && def == first {
			def = first + i
		}
	}
	for i := 0; i < first; i++ {
		args = append(args, fmt.Sprintf("-disposition:s:%d", i), "0")
	}
	args = append(args, fmt.Sprintf("-disposition:s:%d", def), "default", outPath)

	if _, err := runProcess(ctx, ffmpegPath, args...); err != nil {
		return fmt.Errorf("Mux.go: muxSubbed: %w", err)
	}
	return nil
}

// validateOutput is a light sanity check on the muxed output: it must probe
// cleanly and actually carry a subtitle track. Catches a silently broken mux
// (ffmpeg exits 0 but the subtitle stream is missing) before the source is
// treated as done.
func validateOutput(ctx context.Context, path string) error {
	pd, err := probeFile(ctx, path)
	if err != nil {
		return fmt.Errorf("Mux.go: validateOutput: %w", err)
	}
	for _, s := range pd.Streams {
		if s.CodecType == "subtitle" {
			return nil
		}
	}
	return errors.New("Mux.go: validateOutput: muxed output has no subtitle track")
}
