# SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
# Copyright (C) 2026 burnersen
# SPDX-License-Identifier: GPL-3.0-only
"""Fill the mirror release (runtime.json -> mirror.tag) with every mirrored file.

Each file is downloaded from its ORIGINAL source, checked against the SHA-256
pinned in runtime.json, split into parts when it is too large for one release
asset (the same names SuperSubtitler expects: <asset>.part00, .part01, ...) and
uploaded. Assets that are already there with the right size are skipped, so the
workflow can run as often as it likes. Runs in GitHub Actions with `gh`.
"""
import hashlib
import json
import os
import subprocess
import sys
import urllib.request

MANIFEST = json.load(open("runtime.json", encoding="utf-8"))
MIRROR = MANIFEST["mirror"]
TAG = MIRROR["tag"]
WORK = os.environ.get("RUNNER_TEMP", "/tmp") + "/mirror"
os.makedirs(WORK, exist_ok=True)


def gh(*args, capture=False):
    print("+ gh " + " ".join(args), flush=True)
    if capture:
        return subprocess.check_output(["gh", *args], text=True)
    subprocess.check_call(["gh", *args])


def part_names(asset, size):
    if size <= MIRROR["split_over"]:
        return [(asset, 0, size)]
    out, offset, i = [], 0, 0
    while offset < size:
        n = min(MIRROR["part_size"], size - offset)
        out.append(("%s.part%02d" % (asset, i), offset, n))
        offset += n
        i += 1
    return out


def wanted():
    """(asset name, original url, size, sha256) of every mirrored file. An
    empty url marks a file this project builds itself (FFmpeg): it is not
    downloaded here, only checked for presence."""
    items = []
    for key in ("python", "pip", "ffmpeg"):
        f = MANIFEST[key]
        if f.get("mirror"):
            items.append((f["file"], f.get("url", ""), f["size"], f["sha256"]))
    if MANIFEST.get("packages_mirror"):
        for p in MANIFEST["packages"]:
            items.append((p["file"], p["url"], p["size"], p["sha256"]))
    for name, m in MANIFEST["models"].items():
        if not m.get("mirror"):
            continue
        for f in m["files"]:
            url = "https://huggingface.co/%s/resolve/%s/%s" % (m["repo"], m["revision"], f["name"])
            items.append(("%s--%s" % (name, f["name"]), url, f["size"], f["sha256"]))
    return items


def notes():
    lines = [
        "Mirror of the exact runtime files SuperSubtitler installs (see runtime.json).",
        "SuperSubtitler downloads from here first and checks every file against its SHA-256;",
        "the original sources stay the fallback. Not a SuperSubtitler release.",
        "",
        "| File | Version | Source | Licence |",
        "|---|---|---|---|",
    ]
    for key in ("python", "pip", "ffmpeg"):
        f = MANIFEST[key]
        if f.get("mirror"):
            lines.append("| %s | %s | %s | %s |" % (f["file"], f["version"], f.get("url") or "built here, see BUILD.txt in the zip", f.get("license", "")))
    if MANIFEST.get("packages_mirror"):
        lines.append("| %d Python wheels (*.whl) | pinned | https://pypi.org | each wheel's own licence (MIT, BSD, Apache-2.0, PSF; NVIDIA CUDA libraries under the NVIDIA Software License, which allows redistribution with applications) |" % len(MANIFEST["packages"]))
    for name, m in MANIFEST["models"].items():
        lines.append("| %s--* | revision %s | https://huggingface.co/%s | %s |" % (name, m["revision"][:12], m["repo"], m.get("license", "")))
    lines += [
        "",
        "Whisper model weights (c) OpenAI, MIT licence; CTranslate2 conversions by SYSTRAN and Mobius Labs/Dropbox, MIT licence.",
        "Python (c) Python Software Foundation, PSF licence. pip: MIT licence.",
        "FFmpeg: own LGPL build from the unmodified sources ffmpeg-<version>.tar.xz and zlib-<version>.tar.gz, which are in this release too (build script: .github/scripts/build_ffmpeg.sh).",
        "Files larger than 2 GB are split into .partNN pieces; SuperSubtitler joins them while downloading.",
    ]
    return "\n".join(lines)


def ensure_release():
    try:
        gh("release", "view", TAG, "--json", "tagName", capture=True)
    except subprocess.CalledProcessError:
        path = os.path.join(WORK, "notes.md")
        open(path, "w", encoding="utf-8").write(notes())
        # --prerelease and --latest=false keep this out of "latest release",
        # which SuperSubtitlerGUI uses to find SuperSubtitler.exe.
        gh("release", "create", TAG, "--title", "Runtime files (mirror) " + TAG,
           "--notes-file", path, "--prerelease", "--latest=false",
           "--target", os.environ.get("GITHUB_SHA", "HEAD"))


def main():
    ensure_release()
    present = {a["name"]: a["size"] for a in json.loads(gh("release", "view", TAG, "--json", "assets", capture=True))["assets"]}

    for asset, url, size, sha in wanted():
        parts = part_names(asset, size)
        if all(present.get(name) == n for name, _, n in parts):
            print("ok   %s (already mirrored)" % asset, flush=True)
            continue
        if not url:
            sys.exit("MISSING %s: it is built by this project (job 'ffmpeg') and is not in the release with "
                     "the size runtime.json expects (%d bytes)" % (asset, size))
        local = os.path.join(WORK, asset)
        print("get  %s <- %s" % (asset, url), flush=True)
        h = hashlib.sha256()
        req = urllib.request.Request(url, headers={"User-Agent": "SuperSubtitler-mirror"})
        with urllib.request.urlopen(req) as r, open(local, "wb") as out:
            while True:
                chunk = r.read(1 << 20)
                if not chunk:
                    break
                h.update(chunk)
                out.write(chunk)
        if os.path.getsize(local) != size or h.hexdigest() != sha:
            sys.exit("CHECKSUM MISMATCH for %s: got %s (%d bytes), runtime.json says %s (%d bytes)"
                     % (asset, h.hexdigest(), os.path.getsize(local), sha, size))
        files = []
        if len(parts) == 1:
            files = [local]
        else:
            with open(local, "rb") as src:
                for name, offset, n in parts:
                    p = os.path.join(WORK, name)
                    src.seek(offset)
                    with open(p, "wb") as out:
                        left = n
                        while left:
                            chunk = src.read(min(left, 1 << 20))
                            out.write(chunk)
                            left -= len(chunk)
                    files.append(p)
            os.remove(local)
        for p in files:
            gh("release", "upload", TAG, p, "--clobber")
            os.remove(p)
    print("Mirror complete.")


if __name__ == "__main__":
    if sys.argv[1:] == ["--ensure-release"]:
        ensure_release()
    else:
        main()
