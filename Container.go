//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// Container.go — the video file a run writes: MKV (default) or MP4 (-mp4).
//
// MKV takes every stream there is. MP4 plays on more devices (TVs, phones,
// browsers) but only holds a known set of codecs, and subtitles only as plain
// text (mov_text). When a stream that has to go into the file does not fit,
// the file becomes an MKV after all and the user is told why: silently
// dropping a track would lose something nobody chose to lose.

import (
	"context"
	"strings"
)

const (
	containerMKV = "mkv"
	containerMP4 = "mp4"
)

// outputContainer is the container this run asks for: -mp4 / -mkv, else the
// INI's container setting.
func outputContainer(cfg *AppConfig) string {
	if cfg.container != "" {
		return cfg.container
	}
	return appSettings.container
}

// Codecs an MP4 carries well by stream copy. Deliberately conservative:
// FFmpeg can force more into an MP4 (PCM, TrueHD, Vorbis), but players and
// TVs mostly refuse those, and playing everywhere is what -mp4 is for.
var (
	mp4Video = map[string]bool{
		"h264": true, "hevc": true, "av1": true, "vp9": true,
		"mpeg4": true, "mpeg2video": true, "mpeg1video": true,
	}
	mp4Audio = map[string]bool{
		"aac": true, "mp3": true, "mp2": true, "ac3": true, "eac3": true,
		"dts": true, "alac": true, "flac": true, "opus": true,
	}
	// Text subtitles become mov_text; nothing of them is lost.
	mp4TextSubs = map[string]bool{"subrip": true, "mov_text": true, "webvtt": true, "text": true}
)

// mp4CodecNames says in plain words what does not fit, for the usual cases.
var mp4CodecNames = map[string]string{
	"hdmv_pgs_subtitle": "PGS picture subtitles (Blu-ray)",
	"dvd_subtitle":      "VobSub picture subtitles (DVD)",
	"dvb_subtitle":      "DVB picture subtitles",
	"ass":               "ASS/SSA subtitles (their styling would be lost)",
	"ssa":               "ASS/SSA subtitles (their styling would be lost)",
	"truehd":            "Dolby TrueHD audio",
	"vorbis":            "Vorbis audio",
	"vc1":               "VC-1 video",
}

// mp4Problem names why one stream cannot go into an MP4, "" when it can.
func mp4Problem(s probeStream) string {
	codec := strings.ToLower(s.CodecName)
	switch s.CodecType {
	case "video":
		// Cover pictures (MKV attachments show up as such video streams).
		if mp4Video[codec] || (s.Disposition["attached_pic"] == 1 && (codec == "mjpeg" || codec == "png")) {
			return ""
		}
	case "audio":
		if mp4Audio[codec] {
			return ""
		}
		if strings.HasPrefix(codec, "pcm_") {
			return "PCM audio (uncompressed)"
		}
	case "subtitle":
		if mp4TextSubs[codec] {
			return ""
		}
	case "attachment":
		return "attached files (e.g. fonts)"
	case "data":
		return "data tracks"
	}
	if name, ok := mp4CodecNames[codec]; ok {
		return name
	}
	if codec == "" {
		codec = "unknown"
	}
	return codec + " " + s.CodecType
}

// mp4Problems lists, once each, why streams cannot go into an MP4; empty
// means they all fit.
func mp4Problems(streams []probeStream) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range streams {
		if why := mp4Problem(s); why != "" && !seen[why] {
			seen[why] = true
			out = append(out, why)
		}
	}
	return out
}

// keptStreams are the source streams a .subbed file copies: all of them, or
// with -lean the first video stream and the subtitled audio tracks.
func keptStreams(pd *probeData, lean bool, audioOrdinals []int) []probeStream {
	if !lean {
		return pd.Streams
	}
	var out []probeStream
	for _, s := range pd.Streams {
		if s.CodecType == "video" {
			out = append(out, s)
			break
		}
	}
	audio := pd.audioStreams()
	for _, ord := range audioOrdinals {
		if ord >= 0 && ord < len(audio) {
			out = append(out, audio[ord])
		}
	}
	return out
}

// mergeStreams are the streams a merge copies: the video's picture, the
// dropped tracks, and the video's own audio/subtitles where none were dropped.
func mergeStreams(ctx context.Context, video string, audios, subs []string) ([]probeStream, error) {
	pd, err := probeFile(ctx, video)
	if err != nil {
		return nil, err
	}
	var out []probeStream
	for _, s := range pd.Streams {
		if s.CodecType == "video" {
			out = append(out, s)
			break
		}
	}
	for _, s := range pd.Streams {
		if (s.CodecType == "audio" && len(audios) == 0) || (s.CodecType == "subtitle" && len(subs) == 0) {
			out = append(out, s)
		}
	}
	add := func(path, kind string) error {
		p, err := probeFile(ctx, path)
		if err != nil {
			return err
		}
		for _, s := range p.Streams {
			if s.CodecType == kind {
				out = append(out, s)
			}
		}
		return nil
	}
	for _, a := range audios {
		if err := add(a, "audio"); err != nil {
			return nil, err
		}
	}
	for _, s := range subs {
		if err := add(s, "subtitle"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mkvInsteadNote is what the user reads when -mp4 had to become an MKV.
func mkvInsteadNote(why []string) string {
	return "MKV instead of MP4: " + strings.Join(why, ", ") + " cannot go into an MP4"
}

// iso639T maps the two-letter codes Whisper and file names use to the three
// letters an MP4 stores (ISO 639-2/T). FFmpeg writes a two-letter code into
// an MP4 as "undefined", so every subtitle would lose its language.
var iso639T = map[string]string{
	"af": "afr", "am": "amh", "ar": "ara", "as": "asm", "az": "aze", "ba": "bak",
	"be": "bel", "bg": "bul", "bn": "ben", "bo": "bod", "br": "bre", "bs": "bos",
	"ca": "cat", "cs": "ces", "cy": "cym", "da": "dan", "de": "deu", "el": "ell",
	"en": "eng", "es": "spa", "et": "est", "eu": "eus", "fa": "fas", "fi": "fin",
	"fo": "fao", "fr": "fra", "gl": "glg", "gu": "guj", "ha": "hau", "he": "heb",
	"hi": "hin", "hr": "hrv", "ht": "hat", "hu": "hun", "hy": "hye", "id": "ind",
	"is": "isl", "it": "ita", "ja": "jpn", "jv": "jav", "jw": "jav", "ka": "kat",
	"kk": "kaz", "km": "khm", "kn": "kan", "ko": "kor", "la": "lat", "lb": "ltz",
	"ln": "lin", "lo": "lao", "lt": "lit", "lv": "lav", "mg": "mlg", "mi": "mri",
	"mk": "mkd", "ml": "mal", "mn": "mon", "mr": "mar", "ms": "msa", "mt": "mlt",
	"my": "mya", "nb": "nob", "ne": "nep", "nl": "nld", "nn": "nno", "no": "nor",
	"oc": "oci", "pa": "pan", "pl": "pol", "ps": "pus", "pt": "por", "ro": "ron",
	"ru": "rus", "sa": "san", "sd": "snd", "si": "sin", "sk": "slk", "sl": "slv",
	"sn": "sna", "so": "som", "sq": "sqi", "sr": "srp", "su": "sun", "sv": "swe",
	"sw": "swa", "ta": "tam", "te": "tel", "tg": "tgk", "th": "tha", "tk": "tuk",
	"tl": "tgl", "tr": "tur", "tt": "tat", "uk": "ukr", "ur": "urd", "uz": "uzb",
	"vi": "vie", "yi": "yid", "yo": "yor", "zh": "zho",
}

// trackLang is the language tag written for a new track: as it is in an MKV,
// three letters in an MP4 ("und" when the code is unknown).
func trackLang(container, code string) string {
	if container != containerMP4 {
		return code
	}
	code = strings.ToLower(code)
	if len(code) == 3 {
		return code
	}
	if l, ok := iso639T[code]; ok {
		return l
	}
	return "und"
}
