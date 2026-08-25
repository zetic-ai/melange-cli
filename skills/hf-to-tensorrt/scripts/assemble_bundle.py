#!/usr/bin/env python3
"""Assemble engine evidence into a validated zetic.engine_bundle.v1 manifest."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import tempfile
from pathlib import Path
from typing import Any

import validate_bundle


ENGINE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]*$")


def _load(path: Path, *, label: str) -> dict[str, Any]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise ValueError(f"cannot read {label} {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON in {label} {path}: {exc}") from exc
    if not isinstance(payload, dict):
        raise ValueError(f"{label} must contain a JSON object: {path}")
    return payload


def _relative(path: Path, root: Path, *, label: str) -> str:
    try:
        relative = path.resolve().relative_to(root.resolve())
    except ValueError as exc:
        raise ValueError(f"{label} must be inside bundle directory {root}: {path}") from exc
    return relative.as_posix()


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _require(payload: dict[str, Any], key: str, expected: Any, *, label: str) -> None:
    actual = payload.get(key)
    if actual != expected:
        raise ValueError(f"{label}.{key}: expected {expected!r}, observed {actual!r}")


def _load_engine(directory: Path, root: Path) -> dict[str, Any]:
    artifact_dir = directory.resolve()
    if not artifact_dir.is_dir():
        raise ValueError(f"engine evidence directory does not exist: {directory}")
    name = artifact_dir.name
    if not ENGINE_NAME.fullmatch(name):
        raise ValueError(f"invalid engine name inferred from directory: {name!r}")

    build_path = artifact_dir / "build.json"
    metadata_path = artifact_dir / "engine-metadata.json"
    parity_path = artifact_dir / "parity.json"
    build = _load(build_path, label=f"{name} build evidence")
    metadata = _load(metadata_path, label=f"{name} engine metadata")
    parity = _load(parity_path, label=f"{name} parity report")

    _require(build, "schema", "zetic.tensorrt_build.v1", label=f"{name} build")
    _require(build, "status", "succeeded", label=f"{name} build")
    _require(metadata, "schema", "zetic.tensorrt_engine.v1", label=f"{name} metadata")
    _require(parity, "schema", "zetic.engine_parity.v1", label=f"{name} parity")
    _require(parity, "policy", "zetic.engine_parity.v1", label=f"{name} parity")
    _require(parity, "status", "passed", label=f"{name} parity")

    for direction in ("inputs", "outputs"):
        specs = metadata.get(direction)
        if not isinstance(specs, list) or not specs or not all(
            isinstance(spec, dict) for spec in specs
        ):
            raise ValueError(f"{name} metadata.{direction} must be a non-empty list")
        _require(
            build,
            direction,
            specs,
            label=f"{name} ONNX/engine contract",
        )

    engine_file = metadata.get("file")
    if not isinstance(engine_file, str) or Path(engine_file).name != engine_file:
        raise ValueError(f"{name} metadata.file must be a filename")
    engine_path = artifact_dir / engine_file
    if not engine_path.is_file() or engine_path.stat().st_size == 0:
        raise ValueError(f"{name} engine is missing or empty: {engine_path}")
    _require(build, "engine_sha256", metadata.get("sha256"), label=f"{name} build")
    _require(build, "engine_size", metadata.get("size"), label=f"{name} build")

    parity_output_list = parity.get("outputs")
    if not isinstance(parity_output_list, list) or not parity_output_list or not all(
        isinstance(spec, dict) for spec in parity_output_list
    ):
        raise ValueError(f"{name} parity.outputs must be a non-empty list")
    output_specs = {spec.get("name"): spec for spec in metadata["outputs"]}
    parity_outputs = {spec.get("name"): spec for spec in parity_output_list}
    if len(output_specs) != len(metadata["outputs"]):
        raise ValueError(f"{name} engine metadata has duplicate output names")
    if len(parity_outputs) != len(parity_output_list):
        raise ValueError(f"{name} parity report has duplicate output names")
    if output_specs.keys() != parity_outputs.keys():
        raise ValueError(
            f"{name} parity outputs do not match engine outputs: "
            f"engine={sorted(output_specs)}, parity={sorted(parity_outputs)}"
        )
    for output_name, spec in output_specs.items():
        measured = parity_outputs[output_name]
        if measured.get("actual_dtype") != spec.get("dtype"):
            raise ValueError(f"{name} parity dtype does not match {output_name!r}")
        if measured.get("actual_shape") != spec.get("shape"):
            raise ValueError(f"{name} parity shape does not match {output_name!r}")

    typing = build.get("typing")
    if typing not in {"fp16", "strongly_typed"}:
        raise ValueError(f"{name} build has unsupported typing mode: {typing!r}")
    command = build.get("trtexec_command")
    if not isinstance(command, list) or not all(
        isinstance(argument, str) and argument for argument in command
    ):
        raise ValueError(f"{name} build has invalid trtexec_command")

    return {
        "name": name,
        "file": _relative(engine_path, root, label=f"{name} engine"),
        "sha256": metadata.get("sha256"),
        "precision": typing,
        "bindings": {
            "inputs": metadata.get("inputs"),
            "outputs": metadata.get("outputs"),
        },
        "build": {
            "onnx_sha256": build.get("onnx_sha256"),
            "trtexec_command": command,
        },
        "parity": {
            "policy": parity.get("policy"),
            "status": parity.get("status"),
            "fixture_count": parity.get("fixture_count"),
            "report_file": _relative(parity_path, root, label=f"{name} parity report"),
            "report_sha256": _sha256(parity_path),
        },
    }


def assemble(
    *,
    output: Path,
    repo_id: str,
    revision: str,
    architecture: str,
    target_path: Path,
    engine_dirs: list[Path],
) -> dict[str, Any]:
    destination = output.resolve()
    if destination.exists():
        raise ValueError(f"refusing to overwrite existing manifest: {destination}")
    if not engine_dirs:
        raise ValueError("at least one --engine-dir is required")
    target = _load(target_path, label="Thor target evidence")
    engines = [_load_engine(directory, destination.parent) for directory in engine_dirs]
    names = [engine["name"] for engine in engines]
    if len(names) != len(set(names)):
        raise ValueError(f"duplicate engine names: {names}")

    manifest = {
        "schema": "zetic.engine_bundle.v1",
        "source": {
            "repo_id": repo_id,
            "revision": revision,
            "architecture": architecture,
        },
        "target": target,
        "engines": engines,
    }
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary_name: str | None = None
    try:
        with tempfile.NamedTemporaryFile(
            "w",
            encoding="utf-8",
            dir=destination.parent,
            prefix=".engine-bundle.",
            suffix=".json",
            delete=False,
        ) as stream:
            temporary_name = stream.name
            json.dump(manifest, stream, indent=2, sort_keys=True)
            stream.write("\n")
        temporary = Path(temporary_name)
        errors = validate_bundle.validate(temporary)
        if errors:
            raise ValueError("invalid assembled bundle:\n- " + "\n- ".join(errors))
        os.replace(temporary, destination)
        temporary_name = None
    finally:
        if temporary_name is not None:
            Path(temporary_name).unlink(missing_ok=True)
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repo-id", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--architecture", required=True)
    parser.add_argument("--target", type=Path, required=True)
    parser.add_argument("--engine-dir", type=Path, action="append", required=True)
    args = parser.parse_args()
    try:
        manifest = assemble(
            output=args.output,
            repo_id=args.repo_id,
            revision=args.revision,
            architecture=args.architecture,
            target_path=args.target,
            engine_dirs=args.engine_dir,
        )
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    print(f"assembled: {args.output} ({len(manifest['engines'])} engines)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
