//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContainerFlagsAndSetting(t *testing.T) {
	s := appSettings
	defer func() { appSettings = s }()
	appSettings = defaultSettings()

	if got := outputContainer(parseArgs(nil)); got != containerMKV {
		t.Errorf("default = %q, want mkv", got)
	}
	if got := outputContainer(parseArgs([]string{"-MP4"})); got != containerMP4 {
		t.Errorf("-mp4 = %q", got)
	}
	appSettings = parseSettings("container=MP4")
	if settingsProblem(appSettings) != "" || outputContainer(parseArgs(nil)) != containerMP4 {
		t.Errorf("container=mp4 from the INI not taken: %+v", appSettings)
	}
	if got := outputContainer(parseArgs([]string{"-mkv"})); got != containerMKV {
		t.Errorf("-mkv must override the INI, got %q", got)
	}
	if cfg := parseArgs([]string{"-mp4", "-mkv"}); len(cfg.unknownFlags) != 0 || outputContainer(cfg) != containerMKV {
		t.Errorf("the last flag wins and neither is unknown: %+v", cfg)
	}
	if settingsProblem(parseSettings("container=avi")) == "" {
		t.Error("container=avi must be refused")
	}
}

func TestMP4Problems(t *testing.T) {
	fits := []probeStream{
		{CodecType: "video", CodecName: "hevc"},
		{CodecType: "video", CodecName: "mjpeg", Disposition: map[string]int{"attached_pic": 1}},
		{CodecType: "audio", CodecName: "eac3"},
		{CodecType: "audio", CodecName: "aac"},
		{CodecType: "subtitle", CodecName: "subrip"},
	}
	if got := mp4Problems(fits); len(got) != 0 {
		t.Errorf("all of these fit into an MP4, got %q", got)
	}
	bad := append(fits,
		probeStream{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
		probeStream{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
		probeStream{CodecType: "audio", CodecName: "truehd"},
		probeStream{CodecType: "audio", CodecName: "pcm_s24le"},
		probeStream{CodecType: "subtitle", CodecName: "ass"},
		probeStream{CodecType: "attachment", CodecName: "ttf"},
		probeStream{CodecType: "audio", CodecName: "wmav2"},
	)
	want := []string{
		"PGS picture subtitles (Blu-ray)", "Dolby TrueHD audio", "PCM audio (uncompressed)",
		"ASS/SSA subtitles (their styling would be lost)", "attached files (e.g. fonts)", "wmav2 audio",
	}
	if got := mp4Problems(bad); !reflect.DeepEqual(got, want) {
		t.Errorf("problems = %q\nwant       %q (each named once)", got, want)
	}
	if note := mkvInsteadNote(want[:2]); !strings.HasPrefix(note, "MKV instead of MP4: PGS picture subtitles (Blu-ray), Dolby TrueHD audio") {
		t.Errorf("note = %q", note)
	}
}

// -lean only keeps the first video and the subtitled audio tracks, so a
// film whose PGS subtitles and TrueHD track are left out still fits an MP4.
func TestKeptStreamsLean(t *testing.T) {
	pd := &probeData{Streams: []probeStream{
		{Index: 0, CodecType: "video", CodecName: "h264"},
		{Index: 1, CodecType: "audio", CodecName: "truehd"},
		{Index: 2, CodecType: "audio", CodecName: "ac3"},
		{Index: 3, CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
	}}
	if len(mp4Problems(keptStreams(pd, false, []int{1}))) != 2 {
		t.Error("without -lean every stream counts")
	}
	kept := keptStreams(pd, true, []int{1, 7})
	if len(kept) != 2 || kept[0].Index != 0 || kept[1].Index != 2 {
		t.Errorf("lean kept %+v, want the video and the second audio track", kept)
	}
	if got := mp4Problems(kept); len(got) != 0 {
		t.Errorf("lean selection should fit, got %q", got)
	}
}

func TestTrackLang(t *testing.T) {
	cases := []struct{ container, in, want string }{
		{containerMKV, "de", "de"},
		{containerMP4, "de", "deu"},
		{containerMP4, "EN", "eng"},
		{containerMP4, "yue", "yue"},
		{containerMP4, "jw", "jav"},
		{containerMP4, "xx", "und"},
	}
	for _, c := range cases {
		if got := trackLang(c.container, c.in); got != c.want {
			t.Errorf("trackLang(%s, %q) = %q, want %q", c.container, c.in, got, c.want)
		}
	}
}

func TestMergeArgsMP4(t *testing.T) {
	args := strings.Join(mergeArgs("v.mkv", []string{"a.de.m4a"}, []string{"s.en.srt"}, containerMP4, "v.joined.mp4"), " ")
	for _, want := range []string{"-c copy -c:s mov_text", "-metadata:s:a:0 language=deu", "-metadata:s:s:0 language=eng"} {
		if !strings.Contains(args, want) {
			t.Errorf("mp4 merge lacks %q: %s", want, args)
		}
	}
	mkv := strings.Join(mergeArgs("v.mkv", nil, []string{"s.en.srt"}, containerMKV, "v.joined.mkv"), " ")
	if strings.Contains(mkv, "mov_text") || !strings.Contains(mkv, "language=en ") || !strings.HasSuffix(mkv, "v.joined.mkv") {
		t.Errorf("mkv merge changed: %s", mkv)
	}
}

func TestJoinedPathAndExistingMP4(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "Movie")
	if got := uniqueJoinedPath(base, containerMP4); got != base+".joined.mp4" {
		t.Errorf("first = %q", got)
	}
	if err := os.WriteFile(base+".joined.mp4", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueJoinedPath(base, containerMP4); got != base+".joined.2.mp4" {
		t.Errorf("numbered = %q", got)
	}
	if hasExistingSubs(base) {
		t.Error("a .joined.mp4 is no subtitled file")
	}
	if err := os.WriteFile(base+".subbed.mp4", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !hasExistingSubs(base) {
		t.Error("an existing .subbed.mp4 must count as done")
	}
}
