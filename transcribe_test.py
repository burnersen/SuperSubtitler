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


class FakeLogp:
    """Stands in for the model's output of one window: one powerset class
    per frame (0 silence, 1-3 one voice, 4-6 two voices)."""

    def __init__(self, classes):
        self.classes = classes

    def argmax(self, axis):
        return self.classes


class SpeakerChanges(unittest.TestCase):
    def test_a_change_between_two_voices_is_found(self):
        # voice 1 for 1 s, a short pause, voice 2 for 1 s
        frames = [1] * 60 + [0] * 10 + [2] * 60
        changes = transcribe.window_changes(FakeLogp(frames))
        self.assertEqual(len(changes), 1)
        self.assertTrue(1.0 < changes[0] < 1.3, changes)

    def test_one_voice_with_a_pause_is_no_change(self):
        self.assertEqual(transcribe.window_changes(FakeLogp([1] * 60 + [0] * 30 + [1] * 60)), [])

    def test_a_blip_too_short_to_count_is_ignored(self):
        self.assertEqual(transcribe.window_changes(FakeLogp([1] * 60 + [2] * 3 + [1] * 60)), [])

    def test_overlap_in_between_still_counts_as_a_change(self):
        self.assertEqual(len(transcribe.window_changes(FakeLogp([1] * 60 + [4] * 10 + [2] * 60))), 1)

    def test_turns_alternate_between_the_changes(self):
        self.assertEqual(transcribe.turns_from_changes([2.5, 4.0], 6.0), [
            {"start": 0.0, "end": 2.5, "speaker": 0},
            {"start": 2.5, "end": 4.0, "speaker": 1},
            {"start": 4.0, "end": 6.0, "speaker": 0},
        ])
        self.assertEqual(transcribe.turns_from_changes([], 3.0), [{"start": 0.0, "end": 3.0, "speaker": 0}])

    def test_speaker_mode_never_loads_the_whisper_engine(self):
        import inspect
        src = "".join(inspect.getsource(f) for f in (
            transcribe.diarize, transcribe.speaker_changes, transcribe.window_changes, transcribe.read_pcm16))
        self.assertNotIn("faster_whisper", src)
        self.assertNotIn("sherpa", src)


if __name__ == "__main__":
    unittest.main()
