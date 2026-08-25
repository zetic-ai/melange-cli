#!/usr/bin/env python3
"""Apply zetic.engine_parity.v1 to two named-output NPZ files."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
from typing import Any

import numpy as np


POLICY = "zetic.engine_parity.v1"
FLOAT_TOLERANCES = {
    np.dtype("float16"): (1e-2, 1e-2),
    np.dtype("float32"): (1e-4, 1e-4),
    np.dtype("float64"): (1e-4, 1e-4),
}


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _empty_result(name: str, reference: np.ndarray, actual: np.ndarray) -> dict[str, Any]:
    return {
        "name": name,
        "reference_dtype": str(reference.dtype),
        "actual_dtype": str(actual.dtype),
        "reference_shape": list(reference.shape),
        "actual_shape": list(actual.shape),
        "comparison": "unavailable",
        "rtol": None,
        "atol": None,
        "element_count": int(reference.size),
        "mismatch_count": max(int(reference.size), int(actual.size)),
        "nonfinite_count": 0,
        "max_abs_error": None,
        "max_rel_error": None,
        "passed": False,
    }


def _compare(name: str, reference: np.ndarray, actual: np.ndarray) -> dict[str, Any]:
    result = _empty_result(name, reference, actual)
    if reference.shape != actual.shape:
        return result

    if np.issubdtype(reference.dtype, np.floating) and np.issubdtype(
        actual.dtype, np.floating
    ):
        tolerance = FLOAT_TOLERANCES.get(actual.dtype)
        if tolerance is None:
            return result
        rtol, atol = tolerance
        reference64 = reference.astype(np.float64, copy=False)
        actual64 = actual.astype(np.float64, copy=False)
        finite = np.isfinite(reference64) & np.isfinite(actual64)
        nonfinite_count = int(finite.size - np.count_nonzero(finite))
        delta = np.abs(actual64 - reference64)
        close = finite & (delta <= atol + rtol * np.abs(reference64))
        mismatch_count = int(close.size - np.count_nonzero(close))
        if delta.size:
            finite_delta = delta[np.isfinite(delta)]
            max_abs_error = (
                float(np.max(finite_delta)) if finite_delta.size else None
            )
            denominator = np.abs(reference64)
            relative = np.divide(
                delta,
                denominator,
                out=np.full_like(delta, np.inf),
                where=denominator != 0,
            )
            relative[(denominator == 0) & (delta == 0)] = 0
            finite_relative = relative[np.isfinite(relative)]
            max_rel_error = (
                float(np.max(finite_relative)) if finite_relative.size else None
            )
        else:
            max_abs_error = 0.0
            max_rel_error = 0.0
        result.update(
            {
                "comparison": "allclose",
                "rtol": rtol,
                "atol": atol,
                "mismatch_count": mismatch_count,
                "nonfinite_count": nonfinite_count,
                "max_abs_error": max_abs_error,
                "max_rel_error": max_rel_error,
                "passed": mismatch_count == 0 and nonfinite_count == 0,
            }
        )
        return result

    if reference.dtype != actual.dtype:
        return result
    if not (
        np.issubdtype(reference.dtype, np.integer)
        or np.issubdtype(reference.dtype, np.bool_)
    ):
        return result
    equal = reference == actual
    mismatch_count = int(equal.size - np.count_nonzero(equal))
    result.update(
        {
            "comparison": "exact",
            "mismatch_count": mismatch_count,
            "max_abs_error": 0.0 if mismatch_count == 0 else None,
            "max_rel_error": 0.0 if mismatch_count == 0 else None,
            "passed": mismatch_count == 0,
        }
    )
    return result


def compare(reference_path: Path, actual_path: Path) -> dict[str, Any]:
    with np.load(reference_path, allow_pickle=False) as reference_npz, np.load(
        actual_path, allow_pickle=False
    ) as actual_npz:
        reference_names = set(reference_npz.files)
        actual_names = set(actual_npz.files)
        if reference_names != actual_names:
            missing = sorted(reference_names - actual_names)
            unexpected = sorted(actual_names - reference_names)
            raise ValueError(
                f"output names differ; missing={missing}, unexpected={unexpected}"
            )
        outputs = [
            _compare(name, reference_npz[name], actual_npz[name])
            for name in sorted(reference_names)
        ]
    return {
        "schema": POLICY,
        "policy": POLICY,
        "status": "passed" if all(output["passed"] for output in outputs) else "failed",
        "fixture_count": 1,
        "reference_sha256": _sha256(reference_path),
        "actual_sha256": _sha256(actual_path),
        "outputs": outputs,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Compare engine outputs with a reference")
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--actual", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()

    try:
        report = compare(args.reference.resolve(), args.actual.resolve())
    except (OSError, ValueError) as exc:
        parser.error(str(exc))

    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"{report['status']}: {args.report}")
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
