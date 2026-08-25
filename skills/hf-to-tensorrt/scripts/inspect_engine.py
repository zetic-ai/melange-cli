#!/usr/bin/env python3
"""Deserialize a static TensorRT engine and emit binding metadata."""

from __future__ import annotations

import argparse
import hashlib
import importlib
import json
import os
import sys
import types
from pathlib import Path
from typing import Any

import numpy as np


def import_tensorrt() -> Any:
    if "tensorrt" in sys.modules:
        return sys.modules["tensorrt"]
    default = f"/usr/lib/python{sys.version_info.major}.{sys.version_info.minor}/dist-packages"
    system_path = os.environ.get("ZETIC_TENSORRT_PYTHON_PATH", default)
    if system_path and Path(system_path, "tensorrt").is_dir():
        sys.path.insert(0, system_path)
        sys.modules.setdefault("tensorrt_libs", types.ModuleType("tensorrt_libs"))
    return importlib.import_module("tensorrt")


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def binding_specs(engine: Any, trt: Any) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    inputs: list[dict[str, Any]] = []
    outputs: list[dict[str, Any]] = []
    names: set[str] = set()
    for index in range(engine.num_io_tensors):
        name = engine.get_tensor_name(index)
        if name in names:
            raise ValueError(f"duplicate TensorRT binding name: {name!r}")
        names.add(name)
        shape = [int(dimension) for dimension in engine.get_tensor_shape(name)]
        if any(dimension <= 0 for dimension in shape):
            raise ValueError(f"dynamic or invalid TensorRT shape for {name!r}: {shape}")
        dtype = np.dtype(trt.nptype(engine.get_tensor_dtype(name))).name
        spec = {"name": name, "dtype": dtype, "shape": shape}
        target = (
            inputs
            if engine.get_tensor_mode(name) == trt.TensorIOMode.INPUT
            else outputs
        )
        target.append(spec)
    if not inputs or not outputs:
        raise ValueError("TensorRT engine must have at least one input and output")
    return inputs, outputs


def inspect(path: Path) -> dict[str, Any]:
    engine_path = path.resolve()
    if not engine_path.is_file() or engine_path.stat().st_size == 0:
        raise ValueError(f"missing or empty TensorRT engine: {engine_path}")
    trt = import_tensorrt()
    logger = trt.Logger(trt.Logger.ERROR)
    runtime = trt.Runtime(logger)
    engine = runtime.deserialize_cuda_engine(engine_path.read_bytes())
    if engine is None:
        raise ValueError(f"could not deserialize TensorRT engine: {engine_path}")
    inputs, outputs = binding_specs(engine, trt)
    result = {
        "schema": "zetic.tensorrt_engine.v1",
        "file": engine_path.name,
        "size": engine_path.stat().st_size,
        "sha256": _sha256(engine_path),
        "tensorrt_version": trt.__version__,
        "inputs": inputs,
        "outputs": outputs,
    }
    del engine
    del runtime
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = inspect(args.engine)
    except (ImportError, OSError, ValueError) as exc:
        parser.error(str(exc))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"valid: {args.engine} ({len(result['inputs'])} inputs, {len(result['outputs'])} outputs)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
