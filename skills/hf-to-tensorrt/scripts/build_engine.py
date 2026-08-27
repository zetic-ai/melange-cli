#!/usr/bin/env python3
"""Build one v1 static TensorRT engine with native target-board trtexec."""

from __future__ import annotations

import argparse
import hashlib
import json
import shlex
import subprocess
from pathlib import Path
from typing import Any

import inspect_onnx


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def build_command(
    *,
    trtexec: Path,
    onnx_path: Path,
    engine_path: Path,
    typing: str,
    timing_cache: Path | None,
    disable_tf32: bool = False,
) -> list[str]:
    command = [
        str(trtexec),
        f"--onnx={onnx_path}",
        f"--saveEngine={engine_path}",
    ]
    if typing == "fp16":
        command.append("--fp16")
    elif typing == "strongly_typed":
        command.append("--stronglyTyped")
    else:
        raise ValueError(f"unsupported TensorRT typing mode: {typing!r}")
    if disable_tf32:
        command.append("--noTF32")
    if timing_cache is not None:
        command.append(f"--timingCacheFile={timing_cache}")
    command.append("--skipInference")
    return command


def build(
    *,
    onnx_path: Path,
    engine_path: Path,
    trtexec: Path,
    log_path: Path,
    result_path: Path,
    timing_cache: Path | None,
    disable_tf32: bool = False,
) -> dict[str, Any]:
    onnx_metadata = inspect_onnx.inspect(onnx_path)
    if onnx_metadata["has_qdq"]:
        raise ValueError("Q/DQ ONNX graphs are outside the v1 FP16 skill")
    if onnx_metadata["low_bit_dtypes"]:
        raise ValueError(
            f"low-bit ONNX dtypes are outside v1: {onnx_metadata['low_bit_dtypes']}"
        )
    if onnx_metadata["unsupported_dtypes"]:
        raise ValueError(
            "ONNX dtypes outside the v1 FP16/FP32 contract: "
            f"{onnx_metadata['unsupported_dtypes']}"
        )
    executable = trtexec.resolve()
    if not executable.is_file():
        raise ValueError(f"trtexec does not exist: {trtexec}")
    engine = engine_path.resolve()
    if engine.exists():
        raise ValueError(f"refusing to overwrite existing engine: {engine}")
    engine.parent.mkdir(parents=True, exist_ok=True)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    result_path.parent.mkdir(parents=True, exist_ok=True)
    typing = onnx_metadata["recommended_typing"]
    command = build_command(
        trtexec=executable,
        onnx_path=onnx_path.resolve(),
        engine_path=engine,
        typing=typing,
        timing_cache=timing_cache.resolve() if timing_cache is not None else None,
        disable_tf32=disable_tf32,
    )
    completed = subprocess.run(command, check=False, capture_output=True, text=True)
    output = (completed.stdout or "") + (completed.stderr or "")
    log_path.write_text(
        f"$ {shlex.join(command)}\n{output}" + ("" if output.endswith("\n") else "\n"),
        encoding="utf-8",
    )
    result: dict[str, Any] = {
        "schema": "zetic.tensorrt_build.v1",
        "status": "failed",
        "typing": typing,
        "tf32": "disabled" if disable_tf32 else "enabled",
        "onnx_sha256": onnx_metadata["sha256"],
        "trtexec_command": command,
        "inputs": onnx_metadata["inputs"],
        "outputs": onnx_metadata["outputs"],
    }
    if completed.returncode != 0:
        result["error"] = f"trtexec exited {completed.returncode}"
    elif not engine.is_file() or engine.stat().st_size == 0:
        result["error"] = "trtexec succeeded without a non-empty engine"
    else:
        result.update(
            {
                "status": "succeeded",
                "engine_sha256": _sha256(engine),
                "engine_size": engine.stat().st_size,
            }
        )
    result_path.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    if result["status"] != "succeeded":
        raise RuntimeError(result["error"])
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("model", type=Path)
    parser.add_argument("--engine", type=Path, required=True)
    parser.add_argument("--trtexec", type=Path, required=True)
    parser.add_argument("--log", type=Path, required=True)
    parser.add_argument("--result", type=Path, required=True)
    parser.add_argument("--timing-cache", type=Path)
    parser.add_argument(
        "--disable-tf32",
        action="store_true",
        help="append trtexec --noTF32 when default TF32 tactics fail parity",
    )
    args = parser.parse_args()
    try:
        build(
            onnx_path=args.model,
            engine_path=args.engine,
            trtexec=args.trtexec,
            log_path=args.log,
            result_path=args.result,
            timing_cache=args.timing_cache,
            disable_tf32=args.disable_tf32,
        )
    except (OSError, ValueError, RuntimeError) as exc:
        parser.error(str(exc))
    print(f"built: {args.engine}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
