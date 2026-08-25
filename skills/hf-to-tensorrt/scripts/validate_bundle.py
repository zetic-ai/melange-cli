#!/usr/bin/env python3
"""Validate a zetic.engine_bundle.v1 manifest and its local evidence files."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path
from typing import Any

try:
    from jsonschema import Draft202012Validator
except ImportError as exc:  # pragma: no cover - exercised by installation environments
    raise SystemExit(
        "jsonschema is required; run with `uv run --with jsonschema python ...`"
    ) from exc


SCHEMA_PATH = (
    Path(__file__).resolve().parents[1]
    / "references"
    / "engine-bundle.schema.json"
)
PARITY_SCHEMA_PATH = (
    Path(__file__).resolve().parents[1]
    / "references"
    / "engine-parity.schema.json"
)


def _load_json(path: Path, *, label: str) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise ValueError(f"cannot read {label} {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON in {label} {path}: {exc}") from exc


def _json_path(parts: Any) -> str:
    rendered = "$"
    for part in parts:
        if isinstance(part, int):
            rendered += f"[{part}]"
        else:
            rendered += f".{part}"
    return rendered


def _schema_errors(payload: Any, schema: Any) -> list[str]:
    validator = Draft202012Validator(schema)
    errors = sorted(validator.iter_errors(payload), key=lambda err: list(err.path))
    return [f"{_json_path(error.path)}: {error.message}" for error in errors]


def _resolve_evidence(root: Path, relative: str, *, label: str) -> Path:
    raw = Path(relative)
    if raw.is_absolute() or ".." in raw.parts or "\\" in relative:
        raise ValueError(f"{label} must be a safe relative POSIX path: {relative!r}")
    candidate = (root / raw).resolve()
    try:
        candidate.relative_to(root)
    except ValueError as exc:
        raise ValueError(f"{label} escapes the bundle directory: {relative!r}") from exc
    if not candidate.is_file():
        raise ValueError(f"{label} does not exist: {relative}")
    if candidate.stat().st_size == 0:
        raise ValueError(f"{label} is empty: {relative}")
    return candidate


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _require_hash(path: Path, expected: str, *, label: str) -> None:
    actual = _sha256(path)
    if actual != expected:
        raise ValueError(
            f"{label} SHA-256 mismatch: expected {expected}, observed {actual}"
        )


def _semantic_errors(
    payload: dict[str, Any], root: Path, parity_schema: dict[str, Any]
) -> list[str]:
    errors: list[str] = []
    engine_names: set[str] = set()
    engine_files: set[str] = set()

    for index, engine in enumerate(payload["engines"]):
        prefix = f"engines[{index}]"
        name = engine["name"]
        if name in engine_names:
            errors.append(f"{prefix}.name duplicates engine name {name!r}")
        engine_names.add(name)

        engine_file = engine["file"]
        if engine_file in engine_files:
            errors.append(f"{prefix}.file duplicates engine path {engine_file!r}")
        engine_files.add(engine_file)

        try:
            path = _resolve_evidence(root, engine_file, label=f"{prefix}.file")
            _require_hash(path, engine["sha256"], label=f"{prefix}.file")
        except ValueError as exc:
            errors.append(str(exc))

        binding_names: set[str] = set()
        for direction in ("inputs", "outputs"):
            for binding_index, binding in enumerate(engine["bindings"][direction]):
                binding_name = binding["name"]
                if binding_name in binding_names:
                    errors.append(
                        f"{prefix}.bindings.{direction}[{binding_index}].name "
                        f"duplicates binding name {binding_name!r}"
                    )
                binding_names.add(binding_name)

        report_file = engine["parity"]["report_file"]
        try:
            report = _resolve_evidence(
                root, report_file, label=f"{prefix}.parity.report_file"
            )
            _require_hash(
                report,
                engine["parity"]["report_sha256"],
                label=f"{prefix}.parity.report_file",
            )
            report_payload = _load_json(report, label=f"{prefix}.parity report")
            report_schema_errors = _schema_errors(report_payload, parity_schema)
            errors.extend(
                f"{prefix}.parity.report: {error}" for error in report_schema_errors
            )
            if not report_schema_errors:
                if report_payload["status"] != "passed":
                    errors.append(f"{prefix}.parity.report status is not passed")
                if report_payload["policy"] != engine["parity"]["policy"]:
                    errors.append(f"{prefix}.parity policy does not match its report")
                if report_payload["fixture_count"] != engine["parity"]["fixture_count"]:
                    errors.append(
                        f"{prefix}.parity fixture_count does not match its report"
                    )
                engine_outputs = {
                    output["name"]: output for output in engine["bindings"]["outputs"]
                }
                report_outputs = {
                    output["name"]: output for output in report_payload["outputs"]
                }
                if len(report_outputs) != len(report_payload["outputs"]):
                    errors.append(f"{prefix}.parity report has duplicate output names")
                elif engine_outputs.keys() != report_outputs.keys():
                    errors.append(
                        f"{prefix}.parity report outputs do not match engine outputs"
                    )
                else:
                    for output_name, output in engine_outputs.items():
                        measured = report_outputs[output_name]
                        if measured["actual_dtype"] != output["dtype"]:
                            errors.append(
                                f"{prefix}.parity actual dtype for {output_name!r} "
                                "does not match the engine binding"
                            )
                        if measured["actual_shape"] != output["shape"]:
                            errors.append(
                                f"{prefix}.parity actual shape for {output_name!r} "
                                "does not match the engine binding"
                            )
                        if (
                            not measured["passed"]
                            or measured["mismatch_count"] != 0
                            or measured["nonfinite_count"] != 0
                        ):
                            errors.append(
                                f"{prefix}.parity output {output_name!r} did not pass"
                            )
        except ValueError as exc:
            errors.append(str(exc))

    return errors


def validate(manifest_path: Path) -> list[str]:
    manifest = manifest_path.resolve()
    try:
        payload = _load_json(manifest, label="manifest")
        schema = _load_json(SCHEMA_PATH, label="schema")
        parity_schema = _load_json(PARITY_SCHEMA_PATH, label="parity schema")
    except ValueError as exc:
        return [str(exc)]

    errors = _schema_errors(payload, schema)
    if errors:
        return errors
    return _semantic_errors(payload, manifest.parent, parity_schema)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Validate a Zetic TensorRT engine bundle and local evidence"
    )
    parser.add_argument("manifest", type=Path, help="path to engine-bundle.json")
    args = parser.parse_args()

    errors = validate(args.manifest)
    if errors:
        for error in errors:
            print(f"error: {error}", file=sys.stderr)
        return 1

    payload = _load_json(args.manifest.resolve(), label="manifest")
    print(f"valid: {args.manifest} ({len(payload['engines'])} engines)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
