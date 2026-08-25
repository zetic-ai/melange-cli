from __future__ import annotations

import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

import numpy as np

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import inspect_engine  # noqa: E402


class FakeEngine:
    def __init__(self, tensors: list[tuple[str, list[int], str, str]]) -> None:
        self.tensors = tensors
        self.num_io_tensors = len(tensors)

    def get_tensor_name(self, index: int) -> str:
        return self.tensors[index][0]

    def get_tensor_shape(self, name: str) -> list[int]:
        return next(item[1] for item in self.tensors if item[0] == name)

    def get_tensor_dtype(self, name: str) -> str:
        return next(item[2] for item in self.tensors if item[0] == name)

    def get_tensor_mode(self, name: str) -> str:
        return next(item[3] for item in self.tensors if item[0] == name)


TRT = SimpleNamespace(
    TensorIOMode=SimpleNamespace(INPUT="input", OUTPUT="output"),
    nptype=lambda dtype: np.dtype(dtype),
)


class InspectEngineTest(unittest.TestCase):
    def test_extracts_ordered_static_bindings(self) -> None:
        engine = FakeEngine(
            [
                ("input", [1, 4], "float32", "input"),
                ("output", [1, 2], "float16", "output"),
            ]
        )

        inputs, outputs = inspect_engine.binding_specs(engine, TRT)

        self.assertEqual(inputs, [{"name": "input", "dtype": "float32", "shape": [1, 4]}])
        self.assertEqual(outputs, [{"name": "output", "dtype": "float16", "shape": [1, 2]}])

    def test_rejects_dynamic_binding(self) -> None:
        engine = FakeEngine(
            [
                ("input", [-1, 4], "float32", "input"),
                ("output", [1, 2], "float16", "output"),
            ]
        )

        with self.assertRaisesRegex(ValueError, "dynamic"):
            inspect_engine.binding_specs(engine, TRT)

    def test_rejects_duplicate_binding_name(self) -> None:
        engine = FakeEngine(
            [
                ("same", [1], "float32", "input"),
                ("same", [1], "float32", "output"),
            ]
        )

        with self.assertRaisesRegex(ValueError, "duplicate"):
            inspect_engine.binding_specs(engine, TRT)


if __name__ == "__main__":
    unittest.main()
