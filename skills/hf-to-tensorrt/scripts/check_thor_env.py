#!/usr/bin/env python3
"""Capture and optionally verify the Zetic Thor TensorRT build fingerprint."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
from pathlib import Path
from typing import Any


PROFILE = "zetic-thor-v1"


def _run(command: list[str]) -> str | None:
    try:
        completed = subprocess.run(
            command,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
    except OSError:
        return None
    output = completed.stdout.strip()
    return output if completed.returncode == 0 and output else None


def _read(path: Path) -> str | None:
    try:
        value = path.read_bytes().replace(b"\x00", b"").decode("utf-8").strip()
    except (OSError, UnicodeDecodeError):
        return None
    return value or None


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _os_release() -> str | None:
    raw = _read(Path("/etc/os-release"))
    if raw is None:
        return None
    values: dict[str, str] = {}
    for line in raw.splitlines():
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key] = value.strip().strip('"')
    return values.get("PRETTY_NAME") or values.get("VERSION_ID")


def _nvidia_smi_cuda() -> tuple[str, str, str] | None:
    query = _run(
        [
            "nvidia-smi",
            "--query-gpu=name,compute_cap",
            "--format=csv,noheader",
        ]
    )
    summary = _run(["nvidia-smi"])
    if query is None or summary is None:
        return None
    fields = [field.strip() for field in query.splitlines()[0].split(",")]
    if len(fields) != 2 or not fields[0] or not re.fullmatch(r"[0-9]+\.[0-9]+", fields[1]):
        return None
    match = re.search(r"CUDA Version:\s*([0-9]+(?:\.[0-9]+)+)", summary)
    if match is None:
        return None
    return fields[0], fields[1], match.group(1)


def _torch_cuda() -> tuple[str, str, str] | None:
    try:
        import torch
    except ImportError:
        return None
    if not torch.cuda.is_available():
        return None
    major, minor = torch.cuda.get_device_capability(0)
    cuda_version = torch.version.cuda
    if not cuda_version:
        return None
    return torch.cuda.get_device_name(0), f"{major}.{minor}", cuda_version


def _cuda_identity() -> tuple[str, str, str]:
    identity = _nvidia_smi_cuda() or _torch_cuda()
    if identity is None:
        raise RuntimeError(
            "cannot read CUDA device identity from nvidia-smi or torch"
        )
    return identity


def _jetpack_version() -> str | None:
    package = _run(["dpkg-query", "-W", "-f=${Version}", "nvidia-jetpack"])
    if package:
        return package
    return _read(Path("/etc/nv_tegra_release"))


def _driver_version() -> str | None:
    query = _run(
        [
            "nvidia-smi",
            "--query-gpu=driver_version",
            "--format=csv,noheader",
        ]
    )
    if query:
        return query.splitlines()[0].strip()
    return _read(Path("/proc/driver/nvidia/version")) or _read(
        Path("/etc/nv_tegra_release")
    )


def _tensorrt_version(trtexec: Path) -> str | None:
    package = _run(["dpkg-query", "-W", "-f=${Version}", "libnvinfer10"])
    if package:
        return package.split("-", 1)[0]
    output = _run([str(trtexec), "--help"])
    if output is None:
        return None
    match = re.search(r"TensorRT[^0-9]*([0-9]+(?:\.[0-9]+)+)", output, re.IGNORECASE)
    if match:
        return match.group(1)
    compact = re.search(r"TensorRT\s+v([0-9]{5,})", output, re.IGNORECASE)
    if compact:
        digits = compact.group(1)
        return ".".join(
            [str(int(digits[:-4])), str(int(digits[-4:-2])), str(int(digits[-2:]))]
        )
    return None


def capture(trtexec: Path) -> dict[str, Any]:
    executable = trtexec.resolve()
    if not executable.is_file() or not os.access(executable, os.X_OK):
        raise RuntimeError(f"trtexec is not an executable file: {trtexec}")

    cuda_name, compute_capability, cuda_version = _cuda_identity()
    board_model = _read(Path("/proc/device-tree/model"))
    identity = " ".join(filter(None, [board_model, cuda_name]))
    if "thor" not in identity.lower():
        raise RuntimeError(f"the CUDA device does not identify as Thor: {identity!r}")

    values = {
        "gpu_name": board_model or cuda_name,
        "compute_capability": compute_capability,
        "jetpack_version": _jetpack_version(),
        "tensorrt_version": _tensorrt_version(executable),
        "cuda_version": cuda_version,
        "driver_version": _driver_version(),
        "os_release": _os_release(),
        "trtexec_sha256": _sha256(executable),
    }
    missing = sorted(key for key, value in values.items() if not value)
    if missing:
        raise RuntimeError(f"cannot capture required Thor fields: {missing}")
    return {"profile": PROFILE, "fingerprint": values}


def differences(observed: dict[str, Any], expected: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    if observed.get("profile") != expected.get("profile"):
        errors.append(
            f"profile: expected {expected.get('profile')!r}, observed {observed.get('profile')!r}"
        )
    observed_fingerprint = observed.get("fingerprint", {})
    expected_fingerprint = expected.get("fingerprint", {})
    keys = sorted(set(observed_fingerprint) | set(expected_fingerprint))
    for key in keys:
        if observed_fingerprint.get(key) != expected_fingerprint.get(key):
            errors.append(
                f"fingerprint.{key}: expected {expected_fingerprint.get(key)!r}, "
                f"observed {observed_fingerprint.get(key)!r}"
            )
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description="Capture and verify a Thor target")
    parser.add_argument("--trtexec", type=Path, required=True)
    parser.add_argument("--expected", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    try:
        target = capture(args.trtexec)
        if args.expected is not None:
            expected = json.loads(args.expected.read_text(encoding="utf-8"))
            mismatches = differences(target, expected)
            if mismatches:
                raise RuntimeError("Thor profile mismatch:\n- " + "\n- ".join(mismatches))
    except (OSError, json.JSONDecodeError, RuntimeError) as exc:
        parser.error(str(exc))

    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(target, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"captured: {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
