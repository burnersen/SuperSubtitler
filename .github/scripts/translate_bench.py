# SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
# Copyright (C) 2026 burnersen
# SPDX-License-Identifier: GPL-3.0-only
"""Measure how well -to translates.

The test set is FLORES-200 (devtest): sentences translated by professional
translators into every language, CC BY-SA 4.0, checked against its SHA-256.
The English sentences are translated into each target with the code
SuperSubtitler runs (transcribe.py --translate_text) and the pinned model from
runtime.json (mirror first), and compared with the human translations:

    chrF   character n-gram overlap, the main score (higher is better)
    BLEU   word n-gram overlap, the classic score

    translate_bench.py fetch DIR
    translate_bench.py run DIR [--targets de fr ...] [--sentences N]
                               [--json OUT] [--min-chrf L=V ...]
"""
import argparse
import json
import os
import subprocess
import sys
import tarfile
import time

sys.path.insert(0, os.path.dirname(__file__))
from speaker_bench import ROOT, fetch_pinned_model, fetch_url  # noqa: E402

FLORES_URL = "https://dl.fbaipublicfiles.com/nllb/flores200_dataset.tar.gz"
FLORES_SHA = "b8b0b76783024b85797e5cc75064eb83fc5288b41e9654dabc7be6ae944011f6"
FLORES = {"en": "eng_Latn", "de": "deu_Latn", "fr": "fra_Latn", "es": "spa_Latn", "it": "ita_Latn",
          "pt": "por_Latn", "nl": "nld_Latn", "pl": "pol_Latn", "tr": "tur_Latn", "ru": "rus_Cyrl"}


def fetch(folder):
    fetch_pinned_model("translation", os.path.join(folder, "translation"))
    fetch_pinned_model("translation-tokenizer", os.path.join(folder, "translation-tokenizer"))
    tgz = os.path.join(folder, "flores200.tar.gz")
    fetch_url(FLORES_URL, tgz, FLORES_SHA)
    with tarfile.open(tgz) as t:
        t.extractall(folder, filter="data")


def sentences(folder, lang, n):
    path = os.path.join(folder, "flores200_dataset", "devtest", FLORES[lang] + ".devtest")
    return open(path, encoding="utf-8").read().splitlines()[:n]


def evaluate(folder, target, n):
    import sacrebleu
    src, ref = sentences(folder, "en", n), sentences(folder, target, n)
    work = os.path.join(folder, "out")
    os.makedirs(work, exist_ok=True)
    inp, out = os.path.join(work, "in.json"), os.path.join(work, "%s.json" % target)
    json.dump({"texts": src}, open(inp, "w", encoding="utf-8"))
    started = time.time()
    subprocess.run([sys.executable, os.path.join(ROOT, "transcribe.py"), "--translate_text",
                    "--model_dir", os.path.join(folder, "translation"),
                    "--tokenizer", os.path.join(folder, "translation-tokenizer", "tokenizer.json"),
                    "--target", target, "--device", "cpu", "--compute_type", "int8",
                    "--input", inp, "--output", out], check=True, stdout=subprocess.DEVNULL)
    hyp = json.load(open(out, encoding="utf-8"))["translations"]
    return {"target": target, "sentences": n,
            "chrf": round(sacrebleu.corpus_chrf(hyp, [ref]).score, 1),
            "bleu": round(sacrebleu.corpus_bleu(hyp, [ref]).score, 1),
            "seconds": round(time.time() - started)}


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("fetch").add_argument("dir")
    r = sub.add_parser("run")
    r.add_argument("dir")
    r.add_argument("--targets", nargs="*", default=["de", "fr", "es", "it"])
    r.add_argument("--sentences", type=int, default=200)
    r.add_argument("--json")
    r.add_argument("--min-chrf", nargs="*", default=[], help="L=V: fail when chrF into L is below V")
    a = p.parse_args()
    if a.cmd == "fetch":
        fetch(a.dir)
        return
    results = []
    for target in a.targets:
        res = evaluate(a.dir, target, a.sentences)
        print("en->%s: chrF %s, BLEU %s (%s sentences, %ss)" % (target, res["chrf"], res["bleu"], res["sentences"], res["seconds"]), flush=True)
        results.append(res)
    table = "\n".join(["| en → | chrF | BLEU | seconds |", "|---|---|---|---|"] +
                      ["| %s | **%s** | %s | %s |" % (r["target"], r["chrf"], r["bleu"], r["seconds"]) for r in results])
    print(table)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as s:
            s.write("### Translation on FLORES-200 (%d sentences per language)\n\n%s\n" % (a.sentences, table))
    if a.json:
        json.dump(results, open(a.json, "w"), indent=1)
    limits = dict(x.split("=") for x in a.min_chrf)
    low = ["%s: chrF %s < %s" % (r["target"], r["chrf"], limits[r["target"]])
           for r in results if r["target"] in limits and r["chrf"] < float(limits[r["target"]])]
    if low:
        raise SystemExit("translation got worse: " + "; ".join(low))


if __name__ == "__main__":
    main()
