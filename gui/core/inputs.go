// Package core holds the window's logic that does not need Windows: which
// dropped files belong where, how a run's options become CLI arguments, how
// the settings file is read and written back, and how a version is parsed.
// It carries no build tags, so its tests run on every machine.
package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Kind is what a dropped file is to SuperSubtitler.
type Kind string

const (
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindSubtitle Kind = "subtitle"
	KindOther    Kind = "other"
)

// The extension lists mirror the CLI (main.go, Merge.go). A file the CLI would
// ignore must not appear in the window's list as if it were going to be done.
var (
	videoExt = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".m4v": true,
		".ts": true, ".mts": true, ".m2ts": true, ".wmv": true, ".flv": true,
		".webm": true, ".mpg": true, ".mpeg": true, ".vob": true, ".3gp": true,
	}
	audioExt = map[string]bool{
		".m4a": true, ".mp3": true, ".wav": true, ".flac": true,
		".aac": true, ".ogg": true, ".opus": true, ".m4b": true,
	}
	subtitleExt = map[string]bool{
		".srt": true, ".ass": true, ".ssa": true, ".vtt": true,
		".sup": true, ".idx": true, ".sub": true,
	}
)

// KindOf classifies a path by its extension.
func KindOf(path string) Kind {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case videoExt[ext]:
		return KindVideo
	case audioExt[ext]:
		return KindAudio
	case subtitleExt[ext]:
		return KindSubtitle
	}
	return KindOther
}

// IsOwnOutput reports files SuperSubtitler wrote itself (.subbed.mkv,
// .joined.mkv, .joined.N.mkv); the CLI never takes them as input either.
func IsOwnOutput(path string) bool {
	low := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(low, ".subbed.mkv") {
		return true
	}
	stem := strings.TrimSuffix(low, ".mkv")
	if stem == low {
		return false
	}
	if i := strings.LastIndex(stem, ".joined"); i >= 0 {
		rest := stem[i+len(".joined"):]
		if rest == "" {
			return true
		}
		if rest[0] == '.' {
			if _, err := strconv.Atoi(rest[1:]); err == nil {
				return true
			}
		}
	}
	return false
}

// QueueItem is one file in a list of the window.
type QueueItem struct {
	Path   string  `json:"path"`
	Name   string  `json:"name"`
	Dir    string  `json:"dir"`
	Kind   Kind    `json:"kind"`
	SizeMB float64 `json:"sizeMB"`
}

// Rejected names a dropped path that was not taken, and why.
type Rejected struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ExpandPaths turns dropped files and folders into queue items. Folders are
// walked recursively; accept decides which kinds are wanted on the page. The
// result is sorted by folder, then name, and contains every file only once
// (Windows paths are case-insensitive). Files that are not accepted are
// reported back — except inside folders, where unrelated files are normal.
func ExpandPaths(paths []string, accept func(Kind) bool) ([]QueueItem, []Rejected) {
	seen := map[string]bool{}
	var items []QueueItem
	var rejected []Rejected

	add := func(path string, size int64, fromFolder bool) {
		kind := KindOf(path)
		if !accept(kind) {
			if !fromFolder {
				reason := "not a supported file for this page"
				if kind == KindOther {
					reason = "unknown file type"
				}
				rejected = append(rejected, Rejected{Path: path, Reason: reason})
			}
			return
		}
		if IsOwnOutput(path) {
			if !fromFolder {
				rejected = append(rejected, Rejected{Path: path, Reason: "this is a SuperSubtitler result"})
			}
			return
		}
		key := strings.ToLower(filepath.Clean(path))
		if seen[key] {
			return
		}
		seen[key] = true
		items = append(items, QueueItem{
			Path: path, Name: filepath.Base(path), Dir: filepath.Dir(path),
			Kind: kind, SizeMB: float64(size) / (1024 * 1024),
		})
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			rejected = append(rejected, Rejected{Path: p, Reason: "cannot be read"})
			continue
		}
		if !info.IsDir() {
			add(p, info.Size(), false)
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			var size int64
			if fi, err := d.Info(); err == nil {
				size = fi.Size()
			}
			add(path, size, true)
			return nil
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if a, b := strings.ToLower(items[i].Dir), strings.ToLower(items[j].Dir); a != b {
			return a < b
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, rejected
}

// AcceptSubtitleInput takes what the Subtitle page can process.
func AcceptSubtitleInput(k Kind) bool { return k == KindVideo || k == KindAudio }

// AcceptMergeInput takes what the Merge page can combine.
func AcceptMergeInput(k Kind) bool { return k != KindOther }
