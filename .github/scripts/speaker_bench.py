# SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
# Copyright (C) 2026 burnersen
# SPDX-License-Identifier: GPL-3.0-only
"""Measure how well -speakers finds the moments the speaking voice changes.

The test set is a fixed part of VoxConverse (dev): political debates and news
with 1 to 11 speakers, about 66 minutes, with hand-made labels of who speaks
when (CC BY 4.0, https://github.com/joonson/voxconverse). Every audio file is
checked against its SHA-256, so every run measures exactly the same thing.

The speaker marking runs through transcribe.py --diarize - the very code
SuperSubtitler runs - with the pinned model from runtime.json (mirror first).

What subtitles need is the moment the voice changes, so that is what is
scored. A real change lies between the end of one person's speech and the
start of the next one's (consecutive stretches of the same person count as
one); a found change within that gap, give or take 0.5 s, is a hit, each
real and found change used once.

    recall        share of the real changes that were found
    short recall  the same for changes next to speech shorter than 2 s -
                  the quick replies "- " lines are for
    precision     share of the found changes that are real (a false one
                  can put "- " lines where one person speaks)
    F1            recall and precision in one number

    speaker_bench.py fetch DIR [--testset-only] model + test set into DIR
    speaker_bench.py run DIR [--threshold T ...] [--json OUT]
                         [--min-f1 F] [--min-short-recall R]
"""
import argparse
import hashlib
import io
import json
import os
import re
import subprocess
import sys
import time
import urllib.request
import zipfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
MANIFEST = json.load(open(os.path.join(ROOT, "runtime.json"), encoding="utf-8"))

VC_ZIP = "https://www.robots.ox.ac.uk/~vgg/data/voxconverse/data/voxconverse_dev_wav.zip"
VC_LABELS = "https://raw.githubusercontent.com/joonson/voxconverse/24bf60be297701cd7e4ef18550c6d390c1b87365/dev/%s.rttm"
# Two files per speaker count (1..8, one with 11), 2-8 minutes each.
TEST_SET = {
    "ysgbf": "c37a817d940361472c03ddef4a48a87c4276937648206aa921e0dde6f5e832d5",
    "zmndm": "bccd6c7e748fb660ab2a774ef49601f625818f202b64a36017d448c79cccea9c",
    "eqttu": "64260351fd25a48e864c1d7e60afc3b9809dcf72f7dd6610522e17bf6b7449dc",
    "willh": "46e465eb593a0ad832bc8d37622987d50b00f1d8932930e263c420051b27c763",
    "gqdxy": "b43656f57f3ce6e472ee66c112e02ad163ef20f47c6f3cbc36ff7dda2afcce8d",
    "iqbww": "e233ed6bd32d738ff34c1b25237489dfe02d4ee2bb346b3b45394c591779d7f4",
    "mesob": "5fd982a964a4714e323772e067c26fba8413d0d4321a100322306c575405b6b0",
    "txcok": "475979dc3e41db4264262e73b6cc9b66513112b2a67245a4244800b697f7bb6a",
    "afjiv": "208eb00b0dfca6bd149837ca6bc736983f45b4dfe8606973460bd2cadf5f902f",
    "ccokr": "3d7b83e979dfd8de362c46ad24158e32626e9da2f03ab389e33eebf298035537",
    "ggvel": "fb480b9fcec060862fcfc3f37eaeb9088b5d4de6d742940fb2264ddcc8b64720",
    "tguxv": "1b54752ba99398bfb33d26d72d82dfbcdb200ac99e77f4bba45b3f0d86dcf427",
    "luvfz": "0f7f547f9acf4f525b5026838a080d0ebe44ad340a6efa575a48a902580c743a",
    "jyflp": "fd3b7ba27685b0f98a41b16f42de144c1d0929ec560c6f50149b64343728ec81",
    "nrogz": "30c97601ec33f456576403b94ae982f670c5862a369a8711d3ed953c06b588bd",
    "rxgun": "ff8e997f0884fd2e6e91f3579c28fca090b9204ea7684e72a20a3b7261213dee",
}


# ---------------------------------------------------------------------------
# Downloads
# ---------------------------------------------------------------------------

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def retry(fn, tries=5):
    """Servers turn busy clients away now and then; wait and try again. A
    plain "not there" (404 and other 4xx but 429) is final at once."""
    import urllib.error
    for i in range(tries):
        try:
            return fn()
        except OSError as e:  # urllib errors are OSErrors
            final = isinstance(e, urllib.error.HTTPError) and 400 <= e.code < 500 and e.code != 429
            if final or i == tries - 1:
                raise
            print("  %s - retrying in %ss" % (e, 5 * 2 ** i), flush=True)
            time.sleep(5 * 2 ** i)


def fetch_url(url, dest, want=None):
    if os.path.exists(dest) and (want is None or sha256(dest) == want):
        return
    tmp = dest + ".download"

    def get():
        with urllib.request.urlopen(url, timeout=120) as r, open(tmp, "wb") as f:
            for b in iter(lambda: r.read(1 << 20), b""):
                f.write(b)
    retry(get)
    if want is not None and sha256(tmp) != want:
        os.remove(tmp)
        raise SystemExit("checksum mismatch: " + url)
    os.replace(tmp, dest)


def fetch_pinned_model(name, folder):
    """The model files exactly as SuperSubtitler installs them: from the
    mirror first, then from Hugging Face at the pinned revision."""
    m = MANIFEST["models"][name]
    mirror = MANIFEST["mirror"]
    os.makedirs(folder, exist_ok=True)
    for f in m["files"]:
        dest = os.path.join(folder, f["name"])
        urls = []
        if m.get("mirror"):
            urls.append("https://github.com/%s/releases/download/%s/%s--%s" % (mirror["repo"], mirror["tag"], name, f["name"]))
        urls.append("https://huggingface.co/%s/resolve/%s/%s" % (m["repo"], m["revision"], f["name"]))
        for i, url in enumerate(urls):
            try:
                fetch_url(url, dest, f["sha256"])
                break
            except Exception as e:  # noqa: BLE001 - try the next source
                if i == len(urls) - 1:
                    raise
                print("  %s: %s, trying the next source" % (url, e))


class RangeFile(io.RawIOBase):
    """Reads a remote file through HTTP range requests, so single members of
    the 2 GB VoxConverse zip can be taken without downloading all of it."""

    def __init__(self, url):
        self.url = url
        head = urllib.request.Request(url, method="HEAD")
        self.size = int(retry(lambda: urllib.request.urlopen(head, timeout=60)).headers["Content-Length"])
        self.pos = 0

    def seekable(self):
        return True

    def readable(self):
        return True

    def tell(self):
        return self.pos

    def seek(self, off, whence=0):
        self.pos = {0: off, 1: self.pos + off, 2: self.size + off}[whence]
        return self.pos

    def readinto(self, b):
        if self.pos >= self.size or len(b) == 0:
            return 0
        end = min(self.size, self.pos + len(b)) - 1
        req = urllib.request.Request(self.url, headers={"Range": "bytes=%d-%d" % (self.pos, end)})
        data = retry(lambda: urllib.request.urlopen(req, timeout=120).read())
        b[:len(data)] = data
        self.pos += len(data)
        return len(data)


def fetch_test_set(folder):
    os.makedirs(folder, exist_ok=True)
    missing = [n for n, h in TEST_SET.items()
               if not (os.path.exists(os.path.join(folder, n + ".wav")) and sha256(os.path.join(folder, n + ".wav")) == h)]
    if missing:
        z = zipfile.ZipFile(io.BufferedReader(RangeFile(VC_ZIP), buffer_size=1 << 20))
        members = {i.filename.split("/")[-1]: i for i in z.infolist()}
        for n in missing:
            dest = os.path.join(folder, n + ".wav")
            with z.open(members[n + ".wav"]) as src, open(dest, "wb") as dst:
                for b in iter(lambda: src.read(1 << 20), b""):
                    dst.write(b)
            if sha256(dest) != TEST_SET[n]:
                raise SystemExit("checksum mismatch: " + n)
            print("  test audio", n)
    for n in TEST_SET:
        fetch_url(VC_LABELS % n, os.path.join(folder, n + ".rttm"))


# ---------------------------------------------------------------------------
# Scoring
# ---------------------------------------------------------------------------

def read_rttm(path):
    out = []
    for line in open(path):
        p = line.split()
        if p and p[0] == "SPEAKER":
            s = float(p[3])
            out.append((s, s + float(p[4]), p[7]))
    return out


TOLERANCE = 0.5   # s around the gap between two speakers
SHORT = 2.0       # s: a change next to speech this short is a "quick" one


def real_changes(turns):
    """(lo, hi, short) for every change of speaker in time order."""
    runs = []
    for s, e, sp in sorted(turns):
        if runs and runs[-1][2] == sp:
            runs[-1][1] = max(runs[-1][1], e)
        else:
            runs.append([s, e, sp])
    return [(min(a[1], b[0]) - TOLERANCE, max(a[1], b[0]) + TOLERANCE,
             min(a[1] - a[0], b[1] - b[0]) < SHORT) for a, b in zip(runs, runs[1:])]


def match(real, found):
    """Counts of real, found, hit (real found), used (found real), short, short hit."""
    found = sorted(found)
    used = [False] * len(found)
    hits = short = short_hit = 0
    j0 = 0
    for lo, hi, is_short in real:
        short += is_short
        while j0 < len(found) and found[j0] < lo:
            j0 += 1
        j = j0
        while j < len(found) and found[j] <= hi:
            if not used[j]:
                used[j] = True
                hits += 1
                short_hit += is_short
                break
            j += 1
    return {"real": len(real), "found": len(found), "hit": hits, "used": sum(used),
            "short": short, "short_hit": short_hit}


def shipped_threshold():
    """speaker_change_threshold as SuperSubtitler ships it (Config.go)."""
    src = open(os.path.join(ROOT, "Config.go"), encoding="utf-8").read()
    return float(re.search(r"speakerChangeThreshold:\s*([0-9.]+)", src).group(1))


def evaluate(folder, seg, threshold):
    tot = dict.fromkeys(["real", "found", "hit", "used", "short", "short_hit"], 0)
    files, started = [], time.time()
    for n in TEST_SET:
        out = os.path.join(folder, "out", "%s-%s.json" % (n, threshold))
        os.makedirs(os.path.dirname(out), exist_ok=True)
        subprocess.run([sys.executable, os.path.join(ROOT, "transcribe.py"), "--diarize",
                        "--segmentation", seg, "--threshold", str(threshold), "--output", out,
                        os.path.join(folder, n + ".wav")], check=True, stdout=subprocess.DEVNULL)
        r = match(real_changes(read_rttm(os.path.join(folder, n + ".rttm"))), json.load(open(out))["changes"])
        for k in tot:
            tot[k] += r[k]
        files.append(dict(file=n, **r))
    pct = lambda a, b: round(100.0 * a / b, 1) if b else 0.0  # noqa: E731
    precision, recall = pct(tot["used"], tot["found"]), pct(tot["hit"], tot["real"])
    return {
        "threshold": threshold, "precision": precision, "recall": recall,
        "f1": round(2 * precision * recall / (precision + recall), 1) if precision + recall else 0.0,
        "short_recall": pct(tot["short_hit"], tot["short"]),
        "real": tot["real"], "short": tot["short"], "found": tot["found"],
        "seconds": round(time.time() - started), "files": files,
    }


def markdown(results):
    lines = ["| threshold | F1 | recall | short recall | precision | found / real |",
             "|---|---|---|---|---|---|"]
    for r in sorted(results, key=lambda r: -r["f1"]):
        lines.append("| %s | **%s** | %s | %s | %s | %s / %s |" % (
            r["threshold"], r["f1"], r["recall"], r["short_recall"], r["precision"], r["found"], r["real"]))
    return "\n".join(lines)


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("fetch")
    f.add_argument("dir")
    f.add_argument("--testset-only", action="store_true")
    r = sub.add_parser("run")
    r.add_argument("dir")
    r.add_argument("--threshold", type=float, nargs="*", help="default: the shipped speaker_change_threshold")
    r.add_argument("--json", help="write the results here")
    r.add_argument("--min-f1", type=float, help="fail when the F1 of the first threshold is below this")
    r.add_argument("--min-short-recall", type=float, help="fail when its short recall is below this")
    a = p.parse_args()

    seg_dir = os.path.join(a.dir, "speaker-segmentation")
    if a.cmd == "fetch":
        if not a.testset_only:
            fetch_pinned_model("speaker-segmentation", seg_dir)
        fetch_test_set(os.path.join(a.dir, "voxconverse"))
        return
    results = []
    for t in a.threshold or [shipped_threshold()]:
        res = evaluate(os.path.join(a.dir, "voxconverse"), os.path.join(seg_dir, "model.onnx"), t)
        print("threshold %s: F1 %s, recall %s (short %s), precision %s, %ss" % (
            t, res["f1"], res["recall"], res["short_recall"], res["precision"], res["seconds"]), flush=True)
        results.append(res)
    table = markdown(results)
    print(table)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as s:
            s.write("### Speaker changes on VoxConverse (66 min, 1-11 speakers)\n\n" + table + "\n")
    if a.json:
        json.dump(results, open(a.json, "w"), indent=1)
    first = results[0]
    if a.min_f1 is not None and first["f1"] < a.min_f1:
        raise SystemExit("F1 %s is below the limit of %s" % (first["f1"], a.min_f1))
    if a.min_short_recall is not None and first["short_recall"] < a.min_short_recall:
        raise SystemExit("short recall %s is below the limit of %s" % (first["short_recall"], a.min_short_recall))


if __name__ == "__main__":
    main()
