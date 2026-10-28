# SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
# Copyright (C) 2026 burnersen
# SPDX-License-Identifier: GPL-3.0-only
"""Checks for transcribe.py that need neither faster-whisper nor PyAV.

Run: python -m unittest transcribe_test
"""
import sys
import types
import unittest

import transcribe


def fake_av(accepts_metadata_errors):
    calls = []

    known = {"metadata_errors"} if accepts_metadata_errors else set()

    def open_(path, mode="r", **kwargs):
        # Like the real one: any keyword it does not know is a TypeError.
        for k in kwargs:
            if k not in known:
                raise TypeError("open() got an unexpected keyword argument '%s'" % k)
        calls.append((path, kwargs))
        return "container"

    mod = types.ModuleType("av")
    mod.open = open_
    return mod, calls


class PatchPyAVOpen(unittest.TestCase):
    def tearDown(self):
        sys.modules.pop("av", None)

    def test_pyav19_drops_the_removed_keyword(self):
        av, calls = fake_av(accepts_metadata_errors=False)
        sys.modules["av"] = av
        transcribe._patch_pyav_open()
        self.assertEqual(av.open("a.wav", mode="r", metadata_errors="ignore"), "container")
        self.assertEqual(calls, [("a.wav", {})])

    def test_older_pyav_keeps_the_keyword(self):
        av, calls = fake_av(accepts_metadata_errors=True)
        sys.modules["av"] = av
        transcribe._patch_pyav_open()
        av.open("a.wav", mode="r", metadata_errors="ignore")
        self.assertEqual(calls, [("a.wav", {"metadata_errors": "ignore"})])

    def test_other_type_errors_are_not_swallowed(self):
        av, _ = fake_av(accepts_metadata_errors=False)
        sys.modules["av"] = av
        transcribe._patch_pyav_open()
        with self.assertRaises(TypeError):
            av.open("a.wav", bogus=1, metadata_errors="ignore")

    def test_patching_twice_wraps_once(self):
        av, _ = fake_av(accepts_metadata_errors=True)
        sys.modules["av"] = av
        transcribe._patch_pyav_open()
        first = av.open
        transcribe._patch_pyav_open()
        self.assertIs(av.open, first)


class Renumber(unittest.TestCase):
    def test_speakers_are_numbered_by_first_appearance(self):
        turns = [
            {"start": 4.4, "end": 6.9, "speaker": 6},
            {"start": 0.3181, "end": 4.0, "speaker": 11},
            {"start": 7.0, "end": 9.0, "speaker": 11},
            {"start": 9.5, "end": 10.0, "speaker": 0},
        ]
        self.assertEqual(transcribe.renumber(turns), [
            {"start": 0.318, "end": 4.0, "speaker": 0},
            {"start": 4.4, "end": 6.9, "speaker": 1},
            {"start": 7.0, "end": 9.0, "speaker": 0},
            {"start": 9.5, "end": 10.0, "speaker": 2},
        ])

    def test_diarize_mode_never_loads_the_whisper_engine(self):
        # sherpa-onnx brings its own onnxruntime; faster-whisper's silence
        # filter uses another one. They must never share a process.
        import inspect
        src = inspect.getsource(transcribe.diarize) + inspect.getsource(transcribe.read_wav_float)
        self.assertNotIn("faster_whisper", src)
        self.assertNotIn("onnxruntime", src)


if __name__ == "__main__":
    unittest.main()
