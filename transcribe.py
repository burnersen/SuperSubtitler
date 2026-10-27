#!/usr/bin/env python3
"""SuperSubtitler transcription driver.

Invoked by the Go front-end as:

    python.exe transcribe.py [--flag value ...] WAV [WAV ...]
    python.exe transcribe.py --prefetch --model_dir DIR MODEL [MODEL ...]

The second form only downloads the models (one-time setup, so the download
happens with visible progress instead of inside the first transcription) and
prints "@MODELPATH <name><TAB><folder>" for each.

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


def main():
    if "--prefetch" in sys.argv[1:]:
        prefetch()
        return
    args = parse_args()
    _register_cuda_dlls()
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
