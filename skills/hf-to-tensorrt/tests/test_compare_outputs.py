from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import compare_outputs  # noqa: E402


class CompareOutputsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        self.reference = self.root / "reference.npz"
        self.actual = self.root / "actual.npz"

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def test_fp16_tolerance_passes(self) -> None:
        np.savez(self.reference, logits=np.array([1.0, -2.0], dtype=np.float32))
        np.savez(self.actual, logits=np.array([1.005, -2.01], dtype=np.float16))

        report = compare_outputs.compare(self.reference, self.actual)

        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["outputs"][0]["rtol"], 1e-2)

    def test_fp16_outside_tolerance_fails(self) -> None:
        np.savez(self.reference, logits=np.array([1.0, -2.0], dtype=np.float32))
        np.savez(self.actual, logits=np.array([1.1, -2.0], dtype=np.float16))

        report = compare_outputs.compare(self.reference, self.actual)

        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["outputs"][0]["mismatch_count"], 1)

    def test_integer_outputs_are_exact(self) -> None:
        np.savez(self.reference, token=np.array([1, 2], dtype=np.int32))
        np.savez(self.actual, token=np.array([1, 3], dtype=np.int32))

        report = compare_outputs.compare(self.reference, self.actual)

        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["outputs"][0]["comparison"], "exact")

    def test_nonfinite_output_fails(self) -> None:
        np.savez(self.reference, logits=np.array([1.0], dtype=np.float32))
        np.savez(self.actual, logits=np.array([np.nan], dtype=np.float32))

        report = compare_outputs.compare(self.reference, self.actual)

        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["outputs"][0]["nonfinite_count"], 1)

    def test_output_name_mismatch_raises(self) -> None:
        np.savez(self.reference, logits=np.array([1.0], dtype=np.float32))
        np.savez(self.actual, other=np.array([1.0], dtype=np.float32))

        with self.assertRaisesRegex(ValueError, "output names differ"):
            compare_outputs.compare(self.reference, self.actual)


if __name__ == "__main__":
    unittest.main()
