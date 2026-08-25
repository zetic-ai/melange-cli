#!/usr/bin/env python3
"""Validate a static ONNX model and emit TensorRT-oriented metadata."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
from typing import Any, Iterator

import numpy as np
import onnx
from onnx import helper


REDUCED_FLOAT_NAMES = (
    "FLOAT16",
)
LOW_BIT_NAMES = (
    "FLOAT8E4M3FN",
    "FLOAT8E4M3FNUZ",
    "FLOAT8E5M2",
    "FLOAT8E5M2FNUZ",
    "FLOAT4E2M1",
    "INT4",
    "UINT4",
)
OUT_OF_SCOPE_DTYPE_NAMES = (
    "BFLOAT16",
    "DOUBLE",
    "COMPLEX64",
    "COMPLEX128",
    *LOW_BIT_NAMES,
)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _graph_tensors(graph: Any) -> Iterator[Any]:
    yield from graph.initializer
    for sparse in graph.sparse_initializer:
        yield sparse.values
        yield sparse.indices
    for node in graph.node:
        for attribute in node.attribute:
            if attribute.type == onnx.AttributeProto.TENSOR:
                yield attribute.t
            elif attribute.type == onnx.AttributeProto.TENSORS:
                yield from attribute.tensors
            elif attribute.type == onnx.AttributeProto.GRAPH:
                yield from _graph_tensors(attribute.g)
            elif attribute.type == onnx.AttributeProto.GRAPHS:
                for nested in attribute.graphs:
                    yield from _graph_tensors(nested)


def _graph_nodes(graph: Any) -> Iterator[Any]:
    for node in graph.node:
        yield node
        for attribute in node.attribute:
            if attribute.type == onnx.AttributeProto.GRAPH:
                yield from _graph_nodes(attribute.g)
            elif attribute.type == onnx.AttributeProto.GRAPHS:
                for nested in attribute.graphs:
                    yield from _graph_nodes(nested)


def _external_locations(model: Any) -> list[str]:
    locations: list[str] = []
    for tensor in _graph_tensors(model.graph):
        if tensor.data_location != onnx.TensorProto.EXTERNAL:
            continue
        location = next(
            (entry.value for entry in tensor.external_data if entry.key == "location"),
            None,
        )
        if location and location not in locations:
            locations.append(location)
    return locations


def _resolve_external(model_path: Path, locations: list[str]) -> list[dict[str, Any]]:
    root = model_path.parent.resolve()
    result = []
    for location in locations:
        raw = Path(location)
        if raw.is_absolute() or ".." in raw.parts:
            raise ValueError(f"unsafe ONNX external-data location: {location!r}")
        path = (root / raw).resolve()
        try:
            path.relative_to(root)
        except ValueError as exc:
            raise ValueError(
                f"ONNX external-data location escapes model directory: {location!r}"
            ) from exc
        if not path.is_file() or path.stat().st_size == 0:
            raise ValueError(f"missing or empty ONNX external data: {location!r}")
        result.append(
            {"location": location, "size": path.stat().st_size, "sha256": _sha256(path)}
        )
    return result


def _tensor_spec(value: Any) -> dict[str, Any]:
    tensor_type = value.type.tensor_type
    shape = []
    for dimension in tensor_type.shape.dim:
        if not dimension.HasField("dim_value") or dimension.dim_value <= 0:
            marker = dimension.dim_param or "unknown"
            raise ValueError(
                f"tensor {value.name!r} has dynamic dimension {marker!r}"
            )
        shape.append(int(dimension.dim_value))
    dtype = np.dtype(helper.tensor_dtype_to_np_dtype(tensor_type.elem_type)).name
    return {"name": value.name, "dtype": dtype, "shape": shape}


def inspect(model_path: Path) -> dict[str, Any]:
    path = model_path.resolve()
    if not path.is_file() or path.stat().st_size == 0:
        raise ValueError(f"missing or empty ONNX model: {path}")
    model = onnx.load(str(path), load_external_data=False)
    external_data = _resolve_external(path, _external_locations(model))
    onnx.checker.check_model(str(path))

    initializer_names = {tensor.name for tensor in model.graph.initializer}
    inputs = [
        _tensor_spec(value)
        for value in model.graph.input
        if value.name not in initializer_names
    ]
    outputs = [_tensor_spec(value) for value in model.graph.output]
    if not inputs or not outputs:
        raise ValueError("ONNX must have at least one runtime input and output")

    reduced_types = {
        getattr(onnx.TensorProto, name)
        for name in REDUCED_FLOAT_NAMES
        if hasattr(onnx.TensorProto, name)
    }
    low_bit_types = {
        getattr(onnx.TensorProto, name)
        for name in LOW_BIT_NAMES
        if hasattr(onnx.TensorProto, name)
    }
    out_of_scope_types = {
        getattr(onnx.TensorProto, name)
        for name in OUT_OF_SCOPE_DTYPE_NAMES
        if hasattr(onnx.TensorProto, name)
    }
    tensor_types = {tensor.data_type for tensor in _graph_tensors(model.graph)}
    tensor_types.update(
        value.type.tensor_type.elem_type
        for value in (*model.graph.input, *model.graph.output, *model.graph.value_info)
    )
    nodes = list(_graph_nodes(model.graph))
    has_qdq = any(node.op_type in {"QuantizeLinear", "DequantizeLinear"} for node in nodes)
    low_bit = sorted(
        onnx.TensorProto.DataType.Name(dtype) for dtype in tensor_types & low_bit_types
    )
    unsupported_dtypes = sorted(
        onnx.TensorProto.DataType.Name(dtype)
        for dtype in tensor_types & out_of_scope_types
    )
    carries_precision = bool(tensor_types & reduced_types)

    return {
        "schema": "zetic.static_onnx.v1",
        "file": path.name,
        "size": path.stat().st_size,
        "sha256": _sha256(path),
        "external_data": external_data,
        "opsets": [
            {"domain": item.domain, "version": int(item.version)}
            for item in model.opset_import
        ],
        "inputs": inputs,
        "outputs": outputs,
        "has_qdq": has_qdq,
        "low_bit_dtypes": low_bit,
        "unsupported_dtypes": unsupported_dtypes,
        "recommended_typing": "strongly_typed" if carries_precision else "fp16",
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("model", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = inspect(args.model)
    except (OSError, ValueError, onnx.checker.ValidationError) as exc:
        parser.error(str(exc))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"valid: {args.model} ({result['recommended_typing']})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
