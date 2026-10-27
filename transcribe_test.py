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


if __name__ == "__main__":
    unittest.main()
