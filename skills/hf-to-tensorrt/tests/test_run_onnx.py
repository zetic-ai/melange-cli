from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

import numpy as np

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import run_onnx  # noqa: E402


class RunOnnxInputTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def _binding(
        self,
        *,
        name: str = "input",
        dtype: str = "tensor(float16)",
        shape: list[int | str] | None = None,
    ) -> SimpleNamespace:
        return SimpleNamespace(name=name, type=dtype, shape=shape or [1, 2])

    def test_loads_exact_static_fixture(self) -> None:
        path = self.root / "inputs.npz"
        np.savez(path, input=np.ones((1, 2), dtype=np.float16))

        arrays = run_onnx.load_inputs(path, [self._binding()])

        self.assertEqual(list(arrays), ["input"])
        self.assertEqual(arrays["input"].dtype, np.dtype("float16"))
        self.assertTrue(arrays["input"].flags.c_contiguous)

    def test_rejects_different_name_set(self) -> None:
        path = self.root / "inputs.npz"
        np.savez(path, wrong=np.ones((1, 2), dtype=np.float16))

        with self.assertRaisesRegex(ValueError, "input names differ"):
            run_onnx.load_inputs(path, [self._binding()])

    def test_rejects_different_dtype(self) -> None:
        path = self.root / "inputs.npz"
        np.savez(path, input=np.ones((1, 2), dtype=np.float32))

        with self.assertRaisesRegex(ValueError, "input dtype differs"):
            run_onnx.load_inputs(path, [self._binding()])

    def test_rejects_different_shape(self) -> None:
        path = self.root / "inputs.npz"
        np.savez(path, input=np.ones((2, 2), dtype=np.float16))

        with self.assertRaisesRegex(ValueError, "input shape differs"):
            run_onnx.load_inputs(path, [self._binding()])

    def test_rejects_dynamic_binding(self) -> None:
        path = self.root / "inputs.npz"
        np.savez(path, input=np.ones((1, 2), dtype=np.float16))

        with self.assertRaisesRegex(ValueError, "is not static"):
            run_onnx.load_inputs(path, [self._binding(shape=["batch", 2])])


if __name__ == "__main__":
    unittest.main()
