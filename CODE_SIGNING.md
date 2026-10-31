# Code signing policy

Free code signing provided by [SignPath.io](https://about.signpath.io),
certificate by [SignPath Foundation](https://signpath.org).

> **Status:** SuperSubtitler has applied to the SignPath Foundation. Until
> the signing is active, the exes are not signed. Every release says in its
> text whether its exes are signed.

## What is signed

Only these two files, built from the source code in this repository by the
[release workflow](.github/workflows/release.yml) on GitHub-hosted runners:

| File | What it is |
| ---- | ---------- |
| `SuperSubtitler.exe` | The program (command line, drag and drop) |
| `SuperSubtitlerGUI.exe` | The window |

Nothing that was built elsewhere, and nothing from other projects, is signed
with this certificate. Every signing request is approved by hand, release by
release. `SHA256SUMS.txt` is made from the signed files.

Not signed by this project: the runtime that SuperSubtitler downloads on
first run (Python, pip, the Python packages, FFmpeg and the speech models,
listed with their exact versions in [`runtime.json`](runtime.json)). These
are unchanged files of their own projects; the FFmpeg build is made from the
official source by [`.github/scripts/build_ffmpeg.sh`](.github/scripts/build_ffmpeg.sh).

The file properties of both signed exes name the product `SuperSubtitler`
and the version of the release; SignPath refuses to sign files that do not
match.

## Team roles

| Role | Who |
| ---- | --- |
| Committers and reviewers | [burnersen](https://github.com/burnersen) |
| Approvers | [burnersen](https://github.com/burnersen) |

Changes from anybody else (pull requests) are reviewed by a committer before
they are merged. All team members use multi-factor authentication for GitHub
and for SignPath.

## Privacy policy

This program will not transfer any information to other networked systems
unless specifically requested by the user or the person installing or
operating it.

What SuperSubtitler does on the network, and only for that:

- **Runtime download** (first run, `-setup`, or after an update that needs a
  newer runtime): Python, pip, the Python packages, FFmpeg and the speech and
  translation models, each checked against its SHA-256. They come from this
  project's [`runtime-v1` release](https://github.com/burnersen/SuperSubtitler/releases/tag/runtime-v1)
  on GitHub first, and only if that fails from their original sources
  (python.org, pypi.org / files.pythonhosted.org, huggingface.co).
- **Update check of the window:** a few seconds after it starts,
  `SuperSubtitlerGUI.exe` asks the GitHub API for the newest release of
  SuperSubtitler. Nothing is downloaded unless you click *Update*.

Like any network request, these reveal your IP address to the server asked.
Your videos are transcribed and translated **on your own PC**: no audio, no
video and no text ever leaves it. There is no telemetry, no account and no
advertising.

## Installing and uninstalling

There is no installer. SuperSubtitler keeps its settings, the downloaded
runtime and the models in the folder of the exe and writes nothing to the
registry. To uninstall, delete the folder.

## Verifying a download

- **Signature:** right-click the exe > *Properties* > *Digital Signatures*.
  A signed release shows *SignPath Foundation*.
- **Checksums:** every release has a `SHA256SUMS.txt`
  (`Get-FileHash <file>` in PowerShell).

## Reporting a problem

If you think a signed file of this project does something it should not,
please open an [issue](https://github.com/burnersen/SuperSubtitler/issues).
Signed files that break these rules can also be reported to the
[SignPath Foundation](https://signpath.org).
