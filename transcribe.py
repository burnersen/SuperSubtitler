#!/usr/bin/env python3
# SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
# Copyright (C) 2026 burnersen
# SPDX-License-Identifier: GPL-3.0-only
"""SuperSubtitler transcription driver.

Invoked by the Go front-end as:

    python.exe transcribe.py [--flag value ...] WAV [WAV ...]
    python.exe transcribe.py --prefetch --model_dir DIR MODEL [MODEL ...]
    python.exe transcribe.py --diarize --segmentation F [--threshold T] --output F WAV

The second form only downloads the models (one-time setup, so the download
happens with visible progress instead of inside the first transcription) and
prints "@MODELPATH <name><TAB><folder>" for each.

The third form finds the moments the speaking voice changes (see
speaker_changes) and writes {"changes": [t, ...], "turns": [{start, end,
speaker}]} in the WAV's timeline; it needs only onnxruntime and numpy, which
the engine brings anyway, and never loads the speech model.

It mirrors the flag set centralised in Whisper.go and writes, for every input
WAV, an "<output_dir>/<basename>.json" file with exactly the shape the Go side
parses: {"language": str, "segments": [{start, end, text, words:[{start, end,
word}]}]}.

GPU (CUDA) failures exit non-zero with a CUDA-tagged stderr line so the Go
caller's looksLikeGPUError() recognises them and retries the run on CPU.
"""
import argparse
import glob
import json
import os
import sys


def _register_cuda_dlls():
    """Make the pip-installed NVIDIA cuBLAS/cuDNN DLLs discoverable on Windows.

    The nvidia-*-cu12 wheels drop their DLLs in site-packages/nvidia/*/bin,
    which is not on the loader path; CTranslate2 needs them for the CUDA path.
    """
    site_packages = os.path.join(os.path.dirname(sys.executable), "Lib", "site-packages")
    for b in glob.glob(os.path.join(site_packages, "nvidia", "*", "bin")):
        if os.path.isdir(b):
            try:
                os.add_dll_directory(b)
            except OSError:
                pass
            os.environ["PATH"] = b + os.pathsep + os.environ.get("PATH", "")


def _patch_pyav_open():
    """Keep faster-whisper working with PyAV 19 and newer.

    faster-whisper (1.2.1 and older) decodes audio with
    av.open(..., metadata_errors="ignore"), but allows any PyAV >= 11. PyAV 19
    removed that keyword, so a fresh install picks up the newest PyAV and every
    transcription dies with "open() got an unexpected keyword argument
    'metadata_errors'". The keyword only tolerated broken metadata tags; our
    input is a WAV this tool wrote itself, so dropping it changes nothing.
    faster_whisper.audio looks av.open up at call time, so replacing the module
    attribute is enough. Older PyAV keeps getting the keyword unchanged.
    """
    try:
        import av
    except ImportError:
        return
    original = av.open
    if getattr(original, "_ss_compat", False):
        return

    def open_compat(*args, **kwargs):
        try:
            return original(*args, **kwargs)
        except TypeError as e:
            if "metadata_errors" in kwargs and "metadata_errors" in str(e):
                kwargs.pop("metadata_errors")
                return original(*args, **kwargs)
            raise

    open_compat._ss_compat = True
    av.open = open_compat


def _bool(v):
    return str(v).strip().lower() in ("true", "1", "yes", "on")


def parse_args():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--model", required=True)
    p.add_argument("--model_dir", default=None)
    p.add_argument("--language", default=None)
    p.add_argument("--task", default="transcribe")
    p.add_argument("--device", default="cuda")
    p.add_argument("--compute_type", default="float16")
    p.add_argument("--output_dir", required=True)
    p.add_argument("--output_format", default="json")
    p.add_argument("--word_timestamps", type=_bool, default=True)
    p.add_argument("--vad_filter", type=_bool, default=True)
    p.add_argument("--vad_threshold", type=float, default=None)
    p.add_argument("--vad_speech_pad_ms", type=int, default=None)
    p.add_argument("--condition_on_previous_text", type=_bool, default=False)
    p.add_argument("--initial_prompt", default=None)
    p.add_argument("--beam_size", type=int, default=5)
    p.add_argument("inputs", nargs="+")
    return p.parse_args()


def load_model(args):
    from faster_whisper import WhisperModel
    try:
        return WhisperModel(
            args.model,
            device=args.device,
            compute_type=args.compute_type,
            download_root=args.model_dir,
        )
    except Exception as e:
        # Tag stderr so the Go caller recognises a GPU failure and retries on CPU.
        sys.stderr.write("CUDA/GPU init failed on device=%s: %s\n" % (args.device, e))
        raise


def vad_parameters(args):
    params = {}
    if args.vad_threshold is not None:
        params["threshold"] = args.vad_threshold
    if args.vad_speech_pad_ms is not None:
        params["speech_pad_ms"] = args.vad_speech_pad_ms
    return params or None


def transcribe_one(model, args, wav):
    segments, info = model.transcribe(
        wav,
        language=args.language,
        task=args.task,
        beam_size=args.beam_size,
        word_timestamps=args.word_timestamps,
        condition_on_previous_text=args.condition_on_previous_text,
        initial_prompt=args.initial_prompt or None,
        vad_filter=args.vad_filter,
        vad_parameters=vad_parameters(args) if args.vad_filter else None,
    )
    # Progress is reported only for the main run (word_timestamps=True); the Go
    # side ignores it during the short detection run. Fraction = processed audio
    # second / total duration, which is monotonic and tracks real wall-clock.
    emit = bool(args.word_timestamps)
    total = float(getattr(info, "duration", 0) or 0)
    seg_out = []
    for s in segments:  # generator: iterating runs the actual transcription
        words = []
        if s.words:
            for w in s.words:
                words.append({"start": w.start, "end": w.end, "word": w.word})
        seg_out.append({"start": s.start, "end": s.end, "text": s.text, "words": words})
        if emit and total > 0:
            frac = s.end / total
            sys.stdout.write("@PROGRESS %.4f\n" % (1.0 if frac > 1.0 else frac))
            sys.stdout.flush()
    if emit:
        sys.stdout.write("@PROGRESS 1.0000\n")
        sys.stdout.flush()
    return {"language": info.language, "segments": seg_out}


def prefetch():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--prefetch", action="store_true")
    p.add_argument("--model_dir", default=None)
    p.add_argument("models", nargs="+")
    args = p.parse_args()
    from faster_whisper.utils import download_model
    for name in args.models:
        if os.path.isdir(name):
            path = name  # a local model folder: nothing to fetch
        else:
            # The same call WhisperModel() makes on load, so the files land
            # exactly where the later transcription looks for them.
            path = download_model(name, cache_dir=args.model_dir)
        sys.stdout.write("@MODELPATH %s\t%s\n" % (name, path))
        sys.stdout.flush()


# Speaker changes ------------------------------------------------------------
#
# The pyannote segmentation 3.0 model looks at 10 seconds at a time and tells,
# for every 17 ms, which of up to three voices in that stretch is speaking. It
# is trained to tell voices apart exactly there - including a one-second
# reply - while it knows nothing about who is who across the film. Subtitles
# need only that: where the voice changes. Windows start every second, so each
# moment is seen by about nine of them; a change counts when enough of those
# that see it well (not at their very edge) agree. Measured with the speaker
# benchmark (.github/workflows/speaker-benchmark.yml) on hand-labelled
# recordings, this finds more changes - above all short ones - than telling
# voices apart across the film, at the same precision and many times faster.

SEG_RATE = 16000
SEG_WINDOW = 160000           # 10 s, what the model was trained on
SEG_STEP = 16000              # a window every second
SEG_SHIFT, SEG_FIELD = 270, 991   # frame hop and receptive field (samples)
SEG_POWERSET = [(), (0,), (1,), (2,), (0, 1), (0, 2), (1, 2)]
SEG_MIN_RUN = 0.1             # s of one voice before it counts
SEG_EDGE = 0.5                # s at each window edge it cannot judge well
SEG_MERGE = 0.3               # s: votes this close are one change
SEG_MIN_VOTES = 2             # never one window's opinion alone (short clips)


def read_pcm16(path):
    """16-bit mono 16 kHz PCM as int16 (converted per window later, so a long
    film needs no float copy of all of it)."""
    import wave
    import numpy as np
    with wave.open(path, "rb") as w:
        if w.getnchannels() != 1 or w.getsampwidth() != 2 or w.getframerate() != SEG_RATE:
            raise ValueError("%s: need 16-bit mono %d Hz, got %d channel(s), %d bytes, %d Hz"
                             % (path, SEG_RATE, w.getnchannels(), w.getsampwidth(), w.getframerate()))
        return np.frombuffer(w.readframes(w.getnframes()), dtype="<i2")


def window_changes(logp):
    """Change times (s, inside the window) from one window's output: the most
    likely voice combination per frame, runs of one single voice, and a change
    wherever the next such run belongs to another voice (overlap and silence
    in between are skipped)."""
    min_run = max(1, int(SEG_MIN_RUN * SEG_RATE / SEG_SHIFT))
    voice = []
    for c in logp.argmax(-1):
        active = SEG_POWERSET[int(c)]
        voice.append(active[0] if len(active) == 1 else -1)
    runs = []
    start = 0
    for i in range(1, len(voice) + 1):
        if i == len(voice) or voice[i] != voice[start]:
            if voice[start] >= 0 and i - start >= min_run:
                runs.append((voice[start], start, i))
            start = i
    t = lambda f: (f * SEG_SHIFT + SEG_FIELD / 2) / SEG_RATE  # noqa: E731
    return [(t(a_end - 1) + t(b_start)) / 2
            for (va, _, a_end), (vb, b_start, _) in zip(runs, runs[1:]) if va != vb]


def speaker_changes(pcm, session, threshold, progress=None):
    """Times (s) where the speaking voice changes. threshold: the share of the
    windows that see a moment well which must find the change there."""
    import numpy as np
    n = len(pcm)
    starts = list(range(0, max(n - SEG_WINDOW, 0) + 1, SEG_STEP))
    if starts[-1] + SEG_WINDOW < n:
        starts.append(n - SEG_WINDOW)
    events = []
    batch = 16
    for i in range(0, len(starts), batch):
        xs = np.zeros((len(starts[i:i + batch]), 1, SEG_WINDOW), dtype=np.float32)
        for k, s in enumerate(starts[i:i + batch]):
            x = pcm[s:s + SEG_WINDOW]
            xs[k, 0, :len(x)] = x / 32768.0
        for s, logp in zip(starts[i:i + batch], session.run(None, {"x": xs})[0]):
            for t in window_changes(logp):
                if SEG_EDGE <= t <= SEG_WINDOW / SEG_RATE - SEG_EDGE:
                    events.append(s / SEG_RATE + t)
        if progress:
            progress(min(1.0, (i + batch) / len(starts)))
    events.sort()
    groups = []
    for t in events:
        if groups and t - groups[-1][-1] <= SEG_MERGE:
            groups[-1].append(t)
        else:
            groups.append([t])
    begin = np.array(starts) / SEG_RATE
    span = SEG_WINDOW / SEG_RATE
    out = []
    for g in groups:
        t = float(np.median(g))
        # windows that see t well: start in [t - span + edge, t - edge]
        seen = np.searchsorted(begin, t - SEG_EDGE, "right") - np.searchsorted(begin, t - span + SEG_EDGE, "left")
        if len(g) >= SEG_MIN_VOTES and len(g) / seen >= threshold:
            out.append(round(t, 3))
    return out


def turns_from_changes(changes, duration):
    """Stretches between the changes. The numbers only alternate (0, 1, 0 ...):
    a change is known, who speaks is not - and a voice returning after a
    single misjudged word then reads as the same voice again."""
    edges = [0.0] + [c for c in changes if 0 < c < duration] + [duration]
    return [{"start": round(a, 3), "end": round(b, 3), "speaker": i % 2}
            for i, (a, b) in enumerate(zip(edges, edges[1:]))]


def diarize():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--diarize", action="store_true")
    p.add_argument("--segmentation", required=True)
    p.add_argument("--threshold", type=float, default=0.15)
    p.add_argument("--output", required=True)
    p.add_argument("wav")
    args = p.parse_args()
    import onnxruntime as ort
    opts = ort.SessionOptions()
    opts.intra_op_num_threads = max(1, min(os.cpu_count() or 1, 8))
    session = ort.InferenceSession(args.segmentation, opts, providers=["CPUExecutionProvider"])
    pcm = read_pcm16(args.wav)

    def progress(frac):
        sys.stdout.write("@PROGRESS %.4f\n" % frac)
        sys.stdout.flush()

    changes = speaker_changes(pcm, session, args.threshold, progress)
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump({"changes": changes, "turns": turns_from_changes(changes, len(pcm) / SEG_RATE)}, f)
    sys.stdout.write("@PROGRESS 1.0000\n")
    sys.stdout.flush()


def main():
    if "--prefetch" in sys.argv[1:]:
        prefetch()
        return
    if "--diarize" in sys.argv[1:]:
        diarize()
        return
    args = parse_args()
    _register_cuda_dlls()
    _patch_pyav_open()
    os.makedirs(args.output_dir, exist_ok=True)
    model = load_model(args)
    for wav in args.inputs:
        result = transcribe_one(model, args, wav)
        base = os.path.splitext(os.path.basename(wav))[0]
        out_path = os.path.join(args.output_dir, base + ".json")
        with open(out_path, "w", encoding="utf-8") as f:
            json.dump(result, f, ensure_ascii=False)
        # One line per finished input: lets the Go side count a batched
        # language detection and keeps its stall watchdog fed between inputs.
        sys.stdout.write("@DONE %s\n" % base)
        sys.stdout.flush()


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        sys.stderr.write("transcribe.py: %s: %s\n" % (type(e).__name__, e))
        sys.exit(3)
