#!/usr/bin/env python3
"""Execute one static ONNX model from an NPZ fixture."""

from __future__ import annotations

import argparse
from pathlib import Path
from typing import Any

import numpy as np


ORT_DTYPES = {
    "tensor(bool)": np.dtype("bool"),
    "tensor(float)": np.dtype("float32"),
    "tensor(float16)": np.dtype("float16"),
    "tensor(double)": np.dtype("float64"),
    "tensor(int8)": np.dtype("int8"),
    "tensor(int16)": np.dtype("int16"),
    "tensor(int32)": np.dtype("int32"),
    "tensor(int64)": np.dtype("int64"),
    "tensor(uint8)": np.dtype("uint8"),
    "tensor(uint16)": np.dtype("uint16"),
    "tensor(uint32)": np.dtype("uint32"),
    "tensor(uint64)": np.dtype("uint64"),
}


def load_inputs(path: Path, bindings: list[Any]) -> dict[str, np.ndarray[Any, Any]]:
    with np.load(path, allow_pickle=False) as archive:
        arrays = {
            name: np.ascontiguousarray(archive[name]) for name in archive.files
        }
    expected_names = [binding.name for binding in bindings]
    if set(arrays) != set(expected_names):
        raise ValueError(
            f"input names differ: fixture={sorted(arrays)}, "
            f"onnx={sorted(expected_names)}"
        )
    for binding in bindings:
        array = arrays[binding.name]
        if binding.type not in ORT_DTYPES:
            raise ValueError(
                f"unsupported ONNX Runtime input dtype for {binding.name!r}: "
                f"{binding.type}"
            )
        expected_dtype = ORT_DTYPES[binding.type]
        if array.dtype != expected_dtype:
            raise ValueError(
                f"input dtype differs for {binding.name!r}: "
                f"fixture={array.dtype}, onnx={expected_dtype}"
            )
        expected_shape = tuple(binding.shape)
        is_static = all(
            isinstance(dimension, int) and dimension > 0
            for dimension in expected_shape
        )
        if not is_static:
            raise ValueError(
                f"ONNX input {binding.name!r} is not static: {expected_shape}"
            )
        if array.shape != expected_shape:
            raise ValueError(
                f"input shape differs for {binding.name!r}: "
                f"fixture={array.shape}, onnx={expected_shape}"
            )
    return arrays


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("model", type=Path)
    parser.add_argument("--inputs", type=Path, required=True)
    parser.add_argument("--outputs", type=Path, required=True)
    parser.add_argument("--provider", default="CPUExecutionProvider")
    parser.add_argument("--disable-optimizations", action="store_true")
    args = parser.parse_args()

    try:
        import onnxruntime as ort
    except ImportError as error:
        parser.error(
            "onnxruntime is required; run with `uv run --with onnxruntime python ...`"
        )
        raise AssertionError from error

    available_providers = ort.get_available_providers()
    if args.provider not in available_providers:
        parser.error(
            f"provider {args.provider!r} is unavailable; "
            f"available={available_providers}"
        )
    options = ort.SessionOptions()
    if args.disable_optimizations:
        options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_DISABLE_ALL
    session = ort.InferenceSession(
        str(args.model),
        sess_options=options,
        providers=[args.provider],
    )
    inputs = load_inputs(args.inputs, session.get_inputs())
    output_names = [binding.name for binding in session.get_outputs()]
    output_arrays = session.run(output_names, inputs)
    args.outputs.parent.mkdir(parents=True, exist_ok=True)
    np.savez(
        args.outputs,
        **{
            name: np.ascontiguousarray(array)
            for name, array in zip(output_names, output_arrays, strict=True)
        },
    )
    print(f"wrote: {args.outputs}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
