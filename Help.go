//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const helpFileName = "SuperSubtitler_Help.txt"

// helpFileContent is the user-facing manual written next to the executable on
// first run. Plain language, no developer-only switches.
const helpFileContent = `============================================================
  SuperSubtitler - Help
  Automatic subtitles with faster-whisper (GPU, CPU fallback)
============================================================

WHAT IT DOES
  Drag video or audio files (or a whole folder) onto
  SuperSubtitler.exe. For each file it detects the spoken
  language, transcribes the speech and writes:
    - a sidecar subtitle file:  Movie.<lang>.srt
    - a copy of the video with the subtitle track built in:
      Movie.subbed.mkv   (original streams are kept)
  Audio-only files (mp3, m4a, wav, ...) produce just the .srt.

  Subtitles only appear while someone is actually speaking
  (a silence filter keeps quiet parts empty), so there are no
  "stuck" subtitles hanging on screen during pauses.

LANGUAGE
  By default every track is transcribed in ITS OWN language.
  Use -translate to translate everything to English instead
  (English is the only language Whisper can translate to).

QUICK START
  Just drag files or a folder onto the EXE. Already-finished
  files (an existing .srt or .subbed.mkv) are skipped, so
  running again is safe (-force redoes them).

  All questions (which audio tracks, which language when the
  file's tag and the detection disagree) are asked right at
  the start, before the long part begins - you can walk away
  once the progress bars appear.

OPTIONS
  -translate       Translate every track to English.
  -lang:XX         Force the source language (ISO code, e.g.
                   -lang:de, -lang:es). Skips auto-detection.
  -track:N         Use audio track N (1-based) instead of
                   auto-selecting.
  -channel:N       Use physical channel N (0-based) of the
                   chosen track (e.g. to isolate the center).
  -srtonly         Write only the .srt, do not build an MKV.
  -lean            In the MKV keep only video + the chosen
                   audio + the new subtitle (drop other streams).
  -novad           Turn off the silence filter (transcribe
                   everything, including pauses).
  -lowmem          Use a smaller compute type (less video
                   memory, a bit slower).
  -speakers        Mark speaker changes: a new subtitle starts
                   when the voice changes, and a quick exchange
                   shares one subtitle as "- " lines. Loads a
                   small model (6 MB) the first time.
  -split           Export every audio channel as a separate
                   WAV so you can listen and find where the
                   speech is; skips transcription.
  -auto            Never ask anything: all audio tracks, the
                   detected language, merge = yes.
  -force           Redo files that already have subtitles
                   (the old results are overwritten).
  -out:FOLDER      Write the .srt/.mkv results into FOLDER
                   instead of next to the source file.
  -nowait          Do not wait for Enter at the end.
  -setup           Download and install everything (including
                   the speech models) and exit. Useful to get
                   the one-time setup out of the way.
  -nomerge         Never offer the merge mode; subtitle every
                   dropped file.
  -json            Machine-readable progress for front-ends
                   (one JSON event per line on standard output).
  -version         Print the version number and exit.
  -help            Show the banner and the full options list.

  Flags and files may be given in any order.

SUPPORTED FORMATS
  Video -> .subbed.mkv :  mkv, mp4, mov, m4v, avi, ts, m2ts,
                          webm, mpg, wmv, flv, 3gp
  Audio -> .srt only   :  m4a, mp3, wav, flac, aac, ogg,
                          opus, m4b

MERGE (automatic)
  Drop ONE video together with one or more audio and/or
  subtitle files and SuperSubtitler offers to mux them into
  "<video>.joined.mkv" (lossless stream copy, no re-encode):
    - Audio: the dropped audio file(s). If none are dropped,
      the video keeps its own audio (never silent).
    - Subtitles: the dropped subtitle file(s). If none, the
      video keeps its own.
  Dropped tracks are language-tagged from their file names
  (e.g. Movie.de.srt -> German) when recognisable.

SUBTITLE CLEANER
  SuperSubtitler_SRTCleaner.txt (next to the EXE) lists words
  and phrases to delete from the result (ads, hallucinated
  junk, etc.). One term per line; a line starting with "="
  only matches when the whole subtitle equals it exactly.
  Nothing is hard-coded - you decide every term.

FFMPEG
  ffmpeg.exe and ffprobe.exe do all the audio/video work.
  SuperSubtitler downloads its own FFmpeg build (8.1.2, LGPL,
  made from the official source) and checks it; an FFmpeg of another origin
  next to the EXE is replaced by it once, so every install
  works with the same FFmpeg.

CONFIGURATION
  SuperSubtitler_Config.ini (next to the EXE, created on first
  run) holds the models, the silence-filter settings and all
  subtitle formatting limits. Invalid values reset the file to
  its defaults.

FIRST RUN
  On the very first run SuperSubtitler sets itself up: it
  downloads a self-contained Python, the whisper engine and
  the speech models (a few GB, once). They are cached next to
  the EXE; delete the Python\ folder to rebuild from scratch.

  Everything is a fixed, tested set (exact versions, every
  file checked against its SHA-256), fetched from the
  project's own mirror (release runtime-v1 on GitHub)
  first - the same for every user.

REQUIREMENTS
  Windows 10/11 x64. An Nvidia GPU makes it much faster; without
  one it automatically falls back to the (slower) CPU.

  Press Ctrl+C to stop. Press it twice to exit immediately.

EXIT CODES
  0 = everything done, 1 = a file failed or setup failed,
  2 = stopped before the batch was complete.
`

// writeHelpFileIfMissing creates the help file next to the executable when it is
// absent. An existing file is never overwritten. Best-effort: the tool runs
// regardless of whether the file could be written.
func writeHelpFileIfMissing() error {
	path := filepath.Join(exeDir(), helpFileName)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(helpFileContent), 0644); err != nil {
		return fmt.Errorf("Help.go: writeHelpFileIfMissing: %w", err)
	}
	return nil
}
