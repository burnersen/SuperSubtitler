<div align="center">

# 💬 SuperSubtitler

### Drop a video, get clean subtitles back: automatic speech-to-subtitle, drag-and-drop.

**Automatic subtitle generator with faster-whisper, for Windows. International, GPU-accelerated, one EXE.**
Subtitles appear **only while someone is speaking** — no subtitles stuck on screen during silence.

*Powered by [faster-whisper](https://github.com/SYSTRAN/faster-whisper) and [FFmpeg](https://ffmpeg.org), which do the actual speech recognition and audio work. SuperSubtitler is the automation, language logic and safety layer around them.*

[![Windows x64](https://img.shields.io/badge/Windows-10%2F11%20x64-0078D6?logo=windows)](#-requirements)
[![GPU CUDA](https://img.shields.io/badge/GPU-CUDA%20%2B%20CPU%20fallback-76B900?logo=nvidia)](#-requirements)
[![faster-whisper](https://img.shields.io/badge/engine-faster--whisper-blue)](https://github.com/SYSTRAN/faster-whisper)
[![Made with Go](https://img.shields.io/badge/Made%20with-Go-00ADD8?logo=go)](#-building-from-source)
[![License](https://img.shields.io/badge/License-PolyForm%20Noncommercial-blue)](#-license)

**[⬇️ Download the latest release](https://github.com/burnersen/SuperSubtitler/releases/latest)** · **[☕ Buy me a coffee](https://ko-fi.com/T5G0219GA6)**

</div>

```
┌──────────────────────────────────────────────────────────┐
| SuperSubtitler v0.1.0                                    |
| automatic subtitles · faster-whisper · self-setup Python |
└──────────────────────────────────────────────────────────┘

 INFO  [1] Movie.mkv — detecting language...
 INFO  [1] Movie.mkv — transcribe with large-v3-turbo (out: de)...
[1] Movie.mkv transcribing  · ETA 00:00  ████████████████  100% | 1m11s
 SUCCESS  Finished. Done: 1, failed: 0.
```

---

## ⚡ 30 seconds, no manual

1. Download `SuperSubtitler.exe`, a single file with nothing to install.
2. Drag a video or audio file (or a whole folder) onto it.
3. Done. You get a `Movie.<lang>.srt` next to it **and** a `Movie.subbed.mkv` with the subtitle track built in.

On first run SuperSubtitler sets itself up automatically: it downloads FFmpeg, a self-contained Python, the faster-whisper engine and the speech models — no installs, no PATH fiddling, no Python knowledge needed. After that, everything runs offline and locally; your audio never leaves your machine.

---

## ✨ What SuperSubtitler does

- 🔇 **No "sticky" subtitles.** A voice-activity filter means a line shows up **only when someone actually speaks** and disappears in the silence — no caption frozen on screen through a quiet scene. This was the whole reason this tool exists.
- 🌍 **International by default.** Every track is transcribed in **its own language** (German stays German, Spanish stays Spanish…). Add `-translate` to translate everything to English instead.
- ⚡ **GPU-fast, CPU-safe.** Runs on your NVIDIA GPU via auto-installed CUDA libraries (a 1h45 film in ~80 seconds on a modern card), and falls back to the CPU automatically if there's no GPU.
- 🎚️ **Multi-track aware.** A file with several audio tracks (e.g. German + English) lets you pick which ones to subtitle — each becomes its own language-tagged subtitle track in one MKV.
- 🧩 **Merge mode.** Drop one video together with extra audio and/or subtitle files and it offers to mux them into one `.joined.mkv` (lossless copy). The result is never silent — if you don't add audio, the video keeps its own.
- 📐 **Subtitles built right.** Word-level timing, two-line wrapping, sensible characters-per-second, minimum/maximum duration and anti-flicker gaps — close to professional (Netflix/BBC) guidelines.
- 🧹 **SRT cleaner.** A plain-text term list strips ads and hallucinated junk lines (you control every term; nothing is hard-coded).
- 🛡️ **Resilient by design.** Stall watchdog against frozen GPU/engine, per-file crash isolation, output validation, and a Windows Job Object so killing the tool kills every helper it started — no orphan processes.
- 📦 **Ships with its own source.** The EXE carries its own source code (Go `embed`) and extracts it on first run.

---

## 🗣️ Language handling

- **Default:** the spoken language of each track is detected (a sample is taken, split into windows and majority-voted) and the subtitles are written **in that language**.
- **`-translate`:** translates everything to English (the only target Whisper translates to). Uses the large model, which is required for translation.
- **`-lang:XX`:** skip detection and force a source language, e.g. `-lang:de`, `-lang:es`, `-lang:fr`.
- **Mismatch?** If a file's language tag disagrees with what the audio actually is, you're asked which to trust (pressing Enter keeps the detected language).

---

## 🚀 Usage

```
SuperSubtitler.exe [flags] [files/folders]
```

Drag files or folders onto the EXE, or pass them as arguments. Flags and files may be given in any order.

| Flag | Effect |
|---|---|
| *(none)* | Subtitle every dropped video/audio file in its own language |
| `-translate` | Translate every track to English |
| `-lang:XX` | Force the source language (ISO code, e.g. `-lang:de`); skip detection |
| `-track:N` | Use audio track N (1-based) instead of asking/auto-selecting |
| `-channel:N` | Isolate physical channel N (0-based) of the chosen track |
| `-srtonly` | Write only the `.srt`, no MKV |
| `-lean` | In the MKV keep only video + the subtitled audio + the new subtitles |
| `-novad` | Disable the silence filter (transcribe everything, pauses included) |
| `-lowmem` | Smaller compute type (less video memory, a bit slower) |
| `-split` | Export each audio channel as a WAV to find the speech; skip transcription |
| `-help` | Show the banner and full options list |

**Supported input** — Video → `.subbed.mkv`: `mkv mp4 mov m4v avi ts m2ts webm mpg wmv flv 3gp` · Audio → `.srt` only: `m4a mp3 wav flac aac ogg opus m4b`

---

## 🧩 Merge mode

Drop **one** video together with one or more audio and/or subtitle files, and SuperSubtitler offers to mux them into `<video>.joined.mkv` — losslessly, no re-encode:

- **Audio:** the dropped audio file(s). If you don't drop any, the video keeps its own audio (the result is **never** silent).
- **Subtitles:** the dropped subtitle file(s). If you don't drop any, the video keeps its own.
- Dropped tracks are language-tagged from their file names (`Movie.de.srt` → German) when recognisable — so SuperSubtitler's own `.<lang>.srt` outputs slot back in perfectly.

---

## ⚙️ Configuration

Two plain-text files are created next to the EXE on first run:

- **`SuperSubtitler_Config.ini`** — models, the silence-filter settings, lead-in, surround handling and all subtitle formatting limits (chars per line, CPS, min/max duration, …). Invalid values reset the file to its documented defaults.
- **`SuperSubtitler_SRTCleaner.txt`** — one term per line to delete from results (ads, junk). A line starting with `=` only matches when the whole subtitle equals it exactly. Nothing is hard-coded; you decide every term.

---

## 💻 Requirements

- Windows 10/11 x64
- An NVIDIA GPU makes it much faster (CUDA libraries are auto-installed); **without one it automatically falls back to the CPU** (slower, but works)
- FFmpeg, Python and the speech models are downloaded automatically on first run (a few GB, once, cached next to the EXE)
- First run reaches `github.com` (FFmpeg), `python.org`, `pypi.org` and downloads the whisper models

---

## 🛡️ Windows SmartScreen / antivirus warnings

Windows or your antivirus may warn you the first time you run `SuperSubtitler.exe`. The honest reason: **the EXE is not code-signed.** Signing certificates cost hundreds of euros per year, and this is a free hobby project with zero income. Unsigned Go binaries are frequent false-positive targets; there's nothing I can do about it except be transparent.

You don't have to trust me blindly:

- **Scan it:** upload the EXE to [VirusTotal](https://www.virustotal.com) before running it.
- **Read it:** the complete source code is right here in this repository.
- **Build it yourself:** clone, run `build.bat`, done. (The downloaded EXE even carries its own source inside and extracts it on first run.)

If SmartScreen blocks the start: click **"More info" → "Run anyway"**.

---

## 🔨 Building from source

```
build.bat
```

`build.bat` packs the embedded source archive, runs `go mod tidy` and compiles `SuperSubtitler.exe` (Go 1.22+). The only external dependency is [pterm](https://github.com/pterm/pterm). The EXE is written to the parent folder, ready to run next to its downloaded FFmpeg/Python/models. Every released EXE also extracts its own sources on first run.

---

## 🧑‍🎨 The story

SuperSubtitler is a personal hobby project. I wanted subtitles that behave: text when there's speech, nothing when there's silence — no caption stuck on screen through a quiet scene. Once that worked, it grew into a proper international, multi-track tool. It started as a tool just for me, but if it fits your workflow too, all the better.

---

## ❤️ Support

SuperSubtitler is a hobby project, free for personal use. If it saved you hours of typing:

- ⭐ **Star this repo** — fame is the currency here
- ☕ **Buy me a coffee** — keeps the project going:

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/T5G0219GA6)

Found a bug? Open an issue with the console output.

---

## ⚠️ Disclaimer

SuperSubtitler is free hobby software, provided **"as is", without any warranty or condition of any kind**. It was built and tested with care, but you use it **at your own risk**. As far as the applicable law allows, the author is not liable for any damages or data loss arising from the use of this software. See the *No Liability* clause of the [license](LICENSE.md).

---

## 📜 License

**SuperSubtitler is source-available under the [PolyForm Noncommercial License 1.0.0](LICENSE.md).**

That means: free to use, study, modify and share for any **noncommercial** purpose — personal use, hobby, education, research. **Commercial use, resale or bundling into paid products is not permitted** without a separate license from the author. Want a commercial license? Open an issue or reach out.

### Engines & attribution

SuperSubtitler does **not** bundle these; it downloads or invokes them:

- **faster-whisper** — speech recognition engine by [SYSTRAN](https://github.com/SYSTRAN/faster-whisper) (MIT), installed via pip on first run. It runs OpenAI's **Whisper** models (MIT).
- **FFmpeg** — all audio/video processing. On first run an official static build is downloaded from the [BtbN FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds) project (GPL); FFmpeg is a separate work by the [FFmpeg project](https://ffmpeg.org) under its own license and is invoked as an external program.
- **Python** — the official python.org Windows embeddable build, downloaded on first run to host the engine.
