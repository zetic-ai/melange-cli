from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

import onnx
from onnx import TensorProto, helper

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import inspect_onnx  # noqa: E402


def _identity(path: Path, *, dtype: int, shape: list[int | str]) -> None:
    input_value = helper.make_tensor_value_info("input", dtype, shape)
    output_value = helper.make_tensor_value_info("output", dtype, shape)
    graph = helper.make_graph(
        [helper.make_node("Identity", ["input"], ["output"])],
        "identity",
        [input_value],
        [output_value],
    )
    model = helper.make_model(
        graph,
        opset_imports=[helper.make_opsetid("", 17)],
        ir_version=9,
    )
    onnx.save(model, path)


class InspectOnnxTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def test_static_fp32_model_recommends_fp16(self) -> None:
        model_path = self.root / "model.onnx"
        _identity(model_path, dtype=TensorProto.FLOAT, shape=[1, 4])

        result = inspect_onnx.inspect(model_path)

        self.assertEqual(result["recommended_typing"], "fp16")
        self.assertEqual(
            result["inputs"],
            [{"name": "input", "dtype": "float32", "shape": [1, 4]}],
        )
        self.assertFalse(result["has_qdq"])

    def test_static_fp16_model_recommends_strongly_typed(self) -> None:
        model_path = self.root / "model.onnx"
        _identity(model_path, dtype=TensorProto.FLOAT16, shape=[1, 4])

        result = inspect_onnx.inspect(model_path)

        self.assertEqual(result["recommended_typing"], "strongly_typed")

    def test_fp64_is_reported_outside_v1(self) -> None:
        model_path = self.root / "model.onnx"
        _identity(model_path, dtype=TensorProto.DOUBLE, shape=[1, 4])

        result = inspect_onnx.inspect(model_path)

        self.assertEqual(result["unsupported_dtypes"], ["DOUBLE"])

    def test_dynamic_dimension_is_rejected(self) -> None:
        model_path = self.root / "model.onnx"
        _identity(model_path, dtype=TensorProto.FLOAT, shape=["batch", 4])

        with self.assertRaisesRegex(ValueError, "dynamic dimension"):
            inspect_onnx.inspect(model_path)

    def test_qdq_nodes_are_reported(self) -> None:
        model_path = self.root / "model.onnx"
        input_value = helper.make_tensor_value_info("input", TensorProto.FLOAT, [1, 4])
        output_value = helper.make_tensor_value_info("output", TensorProto.FLOAT, [1, 4])
        scale = helper.make_tensor("scale", TensorProto.FLOAT, [], [0.1])
        zero = helper.make_tensor("zero", TensorProto.UINT8, [], [0])
        graph = helper.make_graph(
            [
                helper.make_node("QuantizeLinear", ["input", "scale", "zero"], ["q"]),
                helper.make_node("DequantizeLinear", ["q", "scale", "zero"], ["output"]),
            ],
            "qdq",
            [input_value],
            [output_value],
            [scale, zero],
        )
        onnx.save(
            helper.make_model(
                graph,
                opset_imports=[helper.make_opsetid("", 17)],
                ir_version=9,
            ),
            model_path,
        )

        result = inspect_onnx.inspect(model_path)

        self.assertTrue(result["has_qdq"])


if __name__ == "__main__":
    unittest.main()
