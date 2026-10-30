# 🖥️ SuperSubtitlerGUI

**A window for SuperSubtitler — for everyone who would rather click than type.**
Same engine, same results, same settings files — just visible.

The window never transcribes anything itself. It starts the unchanged
`SuperSubtitler.exe` with `-json`, reads its events and answers its questions.
Every decision about a file (language, timing, layout) stays in the CLI.

## ⚡ Getting started

1. Put `SuperSubtitlerGUI.exe` into the same folder as `SuperSubtitler.exe`
   (no CLI yet? The window fetches the latest release with one click).
2. Start it. The first time, press **Run setup now** — or just press Start:
   FFmpeg, Python, the engine and a speech model are downloaded once, with
   visible progress (MB, MB/s).
3. Drop videos, audio files or whole folders in and press **Start**.

## ✨ What you get

- 🖱️ **Drag and drop** — files or folders, subfolders included.
- ☑️ **Track chooser** — a file with several audio tracks gets a dialog with
  tick boxes instead of typing numbers. Every track was listened to first:
  the **best-understood track of each language** is marked ★ and ticked
  (commentaries and audio descriptions never are). *Use for all following
  files* remembers the choice **by language**: German stays German — the
  best German track — in every file, whatever number it has there. A file without that language still
  asks, so nothing is quietly taken that you did not choose.
- 🗣️ **Language questions as buttons** — "the file says German, it sounds like
  English": one click, or *trust the detection for the rest of this run*.
- 🌙 **All questions come at the start.** SuperSubtitler detects the language
  of every file before the long part begins; once the bars move you can walk
  away. *Never ask* skips every question.
- 📊 **Live progress** — per file: stage, language, percent, time left; overall
  progress in the window title and on the **taskbar button**, which flashes
  when the batch is done.
- 🧩 **Merge page** — one video plus audio/subtitle files → `.joined.mkv`
  or `.joined.mp4`, lossless.
- 📦 **MKV or MP4** — choose the video file on the Subtitle and Merge pages.
  MP4 plays on more TVs and phones; a file with tracks an MP4 cannot hold
  (Blu-ray/DVD subtitles, ASS styling, fonts, TrueHD or PCM sound) becomes
  an MKV anyway, and the file list says why.
- 🔤 **Also as subtitles in** — pick a language on the Subtitle page (`-to:XX`):
  a second subtitle track and `.srt` in that language, same timing, the
  default track in the video file. Translated locally (MADLAD-400, 3 GB one-time).
- 🗨️ **Mark speaker changes** — tick it on the Subtitle page (`-speakers`): a
  new subtitle starts when the voice changes, quick exchanges share one as
  `- ` lines. Each file shows how many speaker changes were found; how sure a
  change must be is on the Settings page.
- 🔊 **Split page** — the voice gets lost? Every channel of an audio track
  becomes its own WAV (`Movie.ch02.wav` …) to listen to; then pick that channel
  under **Audio channel** on the Subtitle page (`-split` / `-channel:N`).
- 🔄 **One-click update** — About → *Update window and SuperSubtitler*: both
  files come from the newest release, checked against its checksums; the
  window replaces itself and restarts. On start it quietly tells you when a
  newer release exists.
- ⚙️ **Settings page** built from `SuperSubtitler_Config.ini` itself: every
  value with a plain-words explanation, the recommended range and its
  default (one click puts it back). Values outside the recommendation are
  marked yellow, values SuperSubtitler would refuse red — those cannot be
  saved. Changing an expert value (the tested runtime) asks first. Before saving, the CLI checks the values
  (`-checkconfig`): a value it would refuse is never written, so the file can
  no longer be reset to defaults by one typo. The previous file stays as `.bak`.
  The subtitle cleaner's term list is editable there too.
  **Restore defaults** brings back SuperSubtitler's standard settings (or the
  standard cleaner list) — written by the CLI itself, your old file kept as
  `.bak`. The Subtitle page's own options have a **Reset** button.
- ✋ **Stop** — the same clean stop as Ctrl+C in the console; press twice to quit
  at once.
- 🌗 Dark and light theme, remembered size and place, one window at a time.

## 🔧 Building & tests

```
build.bat              :: in this folder; writes ..\..\SuperSubtitlerGUI.exe
```

or `wails build`. The window is `frontend/dist/index.html` plus `logic.js` —
no npm, no bundler.

- `go test ./...` — `core/` (arguments, file lists, INI view, versions) has no
  build tags and runs on any OS; the rest is Windows-only.
- `node frontend/logic_check.js` and `node frontend/wiring_check.js` check the
  window's script without a browser.

CI runs all of it on Windows.
