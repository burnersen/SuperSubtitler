//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/pterm/pterm"
)

// Progress.go — visible progress for the one-time setup (several GB of
// downloads). Without it the first start looks frozen for many minutes behind
// a spinner that only says "downloading".

const bytesPerMB = 1024 * 1024

// dlProgress is an io.Writer that only counts: it sits beside the real file in
// an io.MultiWriter and reports the running total at most four times a second,
// to the console spinner and as a "setup" event.
type dlProgress struct {
	step    string
	label   string
	total   int64 // 0 = unknown (no Content-Length)
	done    int64
	started time.Time
	last    time.Time
	spinner *pterm.SpinnerPrinter
}

func newDLProgress(step, label string, total int64, spinner *pterm.SpinnerPrinter) *dlProgress {
	now := time.Now()
	p := &dlProgress{step: step, label: label, total: total, started: now, spinner: spinner}
	emitSetup(step, "start", label, 0, float64(total)/bytesPerMB, 0)
	return p
}

func (p *dlProgress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) >= 250*time.Millisecond {
		p.last = time.Now()
		p.report()
	}
	return len(b), nil
}

func (p *dlProgress) rateMBps() float64 {
	secs := time.Since(p.started).Seconds()
	if secs <= 0 {
		return 0
	}
	return float64(p.done) / bytesPerMB / secs
}

func (p *dlProgress) report() {
	rate := p.rateMBps()
	if p.spinner != nil {
		p.spinner.UpdateText(progressText(p.label, p.done, p.total, rate))
	}
	emitSetup(p.step, "progress", p.label, float64(p.done)/bytesPerMB, float64(p.total)/bytesPerMB, rate)
}

// finish sends the final numbers, so a front-end never stays at 97 %.
func (p *dlProgress) finish() {
	emitSetup(p.step, "done", p.label, float64(p.done)/bytesPerMB, float64(p.total)/bytesPerMB, p.rateMBps())
}

// progressText renders "Downloading FFmpeg — 45.2 / 120.0 MB (38%) · 5.1 MB/s",
// or without the total and percentage when the size is unknown.
func progressText(label string, done, total int64, mbps float64) string {
	doneMB := float64(done) / bytesPerMB
	text := label + " — "
	if total > 0 {
		pct := float64(done) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
		text += fmt.Sprintf("%.1f / %.1f MB (%.0f%%)", doneMB, float64(total)/bytesPerMB, pct)
	} else {
		text += fmt.Sprintf("%.1f MB", doneMB)
	}
	if mbps > 0 {
		text += fmt.Sprintf(" · %.1f MB/s", mbps)
	}
	return text
}

// dirSize sums the sizes of all files below root (unreadable entries are
// skipped). Used to watch a model download whose own progress is not visible.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
