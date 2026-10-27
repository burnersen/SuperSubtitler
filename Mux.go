//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
)

// subInput is one SRT sidecar plus the language tag it should carry in the MKV.
type subInput struct {
	path string
	lang string
}

// buildMKV soft-muxes one or more SRT sidecars into srcVideo, each as a
// language-tagged subtitle track (the first is the default). In full mode every
// source stream is copied losslessly; in -lean mode only the video, the
// subtitled audio tracks (audioOrdinals) and the new subtitles survive.
func buildMKV(ctx context.Context, cfg *AppConfig, srcVideo string, subs []subInput, audioOrdinals []int, outPath string) error {
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
	args = append(args, "-c", "copy", "-c:s", "srt")
	for i, s := range subs {
		args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "language="+s.lang)
	}
	args = append(args, "-disposition:s:0", "default", outPath)

	if _, err := runProcess(ctx, ffmpegPath, args...); err != nil {
		return fmt.Errorf("Mux.go: buildMKV: %w", err)
	}
	if err := copyTimestamps(srcVideo, outPath); err != nil {
		// Non-fatal: the MKV is valid, only its file dates differ.
		pWarn.Println("Could not copy timestamps to output: " + shortErr(err))
	}
	if err := validateMKV(ctx, outPath); err != nil {
		return err
	}
	return nil
}

// validateMKV is a light sanity check on the muxed output: it must probe cleanly
// and actually carry a subtitle track. Catches a silently broken mux (ffmpeg
// exits 0 but the subtitle stream is missing) before the source is treated as done.
func validateMKV(ctx context.Context, path string) error {
	pd, err := probeFile(ctx, path)
	if err != nil {
		return fmt.Errorf("Mux.go: validateMKV: %w", err)
	}
	for _, s := range pd.Streams {
		if s.CodecType == "subtitle" {
			return nil
		}
	}
	return errors.New("Mux.go: validateMKV: muxed output has no subtitle track")
}
