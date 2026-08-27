#!/usr/bin/env python3
"""Package one validated Thor engine bundle into one encrypted ZTC."""

from __future__ import annotations

import argparse
import hashlib
import importlib
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from types import ModuleType
from typing import Any


METADATA_VERSION = "0.3.0"
TARGET = "TENSORRT_FP16"
RESULT_ID = "tensorrt-fp16"
SAFE_KEY = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]*$")
COMPATIBILITY = {
    "ap_types": ["GPU"],
    "os": None,
    "soc_manufacturer": "nvidia",
    "soc_model": None,
}


@dataclass(frozen=True)
class PackageResult:
    ztc_path: Path
    metadata_path: Path
    private_manifest_path: Path
    report_path: Path


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_bundle_validator(manifest: Path, validator: Path) -> None:
    if not validator.is_file():
        raise ValueError(
            "hf-to-tensorrt bundle validator is unavailable; install both skills"
        )
    completed = subprocess.run(
        [sys.executable, str(validator), str(manifest)],
        capture_output=True,
        text=True,
        check=False,
    )
    if completed.returncode != 0:
        detail = (completed.stdout + completed.stderr).strip()
        raise ValueError(f"engine bundle validation failed:\n{detail}")


def _safe_artifact(root: Path, relative: Any) -> Path:
    if not isinstance(relative, str) or not relative:
        raise ValueError("engine file path is missing")
    path = (root / relative).resolve()
    try:
        path.relative_to(root.resolve())
    except ValueError as exc:
        raise ValueError("engine file escapes the bundle directory") from exc
    if not path.is_file() or path.stat().st_size == 0:
        raise ValueError(f"engine file is missing or empty: {relative}")
    return path


def _validate_bindings(engine: dict[str, Any]) -> None:
    bindings = engine.get("bindings")
    if not isinstance(bindings, dict):
        raise ValueError("engine bindings are missing")
    names: set[str] = set()
    for kind in ("inputs", "outputs"):
        tensors = bindings.get(kind)
        if not isinstance(tensors, list) or not tensors:
            raise ValueError(f"engine {kind} bindings must be non-empty")
        for tensor in tensors:
            if not isinstance(tensor, dict):
                raise ValueError(f"engine {kind} binding must be an object")
            name = tensor.get("name")
            dtype = tensor.get("dtype")
            shape = tensor.get("shape")
            if not isinstance(name, str) or not name or name in names:
                raise ValueError("binding names must be non-empty and unique")
            if not isinstance(dtype, str) or not dtype:
                raise ValueError(f"binding {name!r} has no dtype")
            if not isinstance(shape, list) or not all(
                isinstance(dim, int) and not isinstance(dim, bool) and dim > 0
                for dim in shape
            ):
                raise ValueError(f"binding {name!r} is not fully static")
            names.add(name)


def select_engine(bundle: dict[str, Any], bundle_root: Path) -> tuple[dict, Path]:
    if bundle.get("schema") != "zetic.engine_bundle.v1":
        raise ValueError("unsupported engine bundle schema")
    target = bundle.get("target")
    if not isinstance(target, dict) or target.get("profile") != "zetic-thor-v1":
        raise ValueError("ZTC packaging supports only the exact zetic-thor-v1 profile")
    engines = bundle.get("engines")
    if not isinstance(engines, list) or len(engines) != 1:
        raise ValueError("one ZTC requires exactly one engine entry")
    engine = engines[0]
    if not isinstance(engine, dict):
        raise ValueError("engine entry must be an object")
    if engine.get("precision") not in {"fp16", "strongly_typed"}:
        raise ValueError("ZTC v1 supports only FP16 or strongly typed engines")
    _validate_bindings(engine)
    engine_path = _safe_artifact(bundle_root, engine.get("file"))
    expected_hash = engine.get("sha256")
    if not isinstance(expected_hash, str) or sha256(engine_path) != expected_hash:
        raise ValueError("engine SHA-256 does not match the bundle")
    return engine, engine_path


def target_model_id(module_name: str, engine_filename: str) -> str:
    identity = {
        "compatibility": COMPATIBILITY,
        "destination": TARGET,
        "file_name": engine_filename,
        "module_name": module_name,
        "preserve_io_datatype": None,
    }
    return hashlib.sha256(
        json.dumps(identity, sort_keys=True).encode("utf-8")
    ).hexdigest()[:8]


def package_key(
    package_base_key: str, source_target_model_ids: dict[str, str]
) -> str:
    source = "|".join(
        f"{name}:{model_id}"
        for name, model_id in sorted(source_target_model_ids.items())
    )
    digest = hashlib.sha256(source.encode("utf-8")).hexdigest()[:8]
    return f"{package_base_key}_{digest}"


def _tensor_metadata(tensor: dict[str, Any]) -> dict[str, Any]:
    shape = tensor["shape"]
    return {
        "dtype": tensor["dtype"],
        "name": tensor["name"],
        "original_name": tensor["name"],
        "rank": len(shape),
        "shape": shape,
    }


def build_metadata(
    bundle: dict[str, Any], engine: dict[str, Any], package_base_key: str
) -> tuple[dict[str, Any], str]:
    if not SAFE_KEY.fullmatch(package_base_key):
        raise ValueError("package base key contains unsupported characters")
    source = bundle.get("source")
    if not isinstance(source, dict):
        raise ValueError("bundle source metadata is missing")
    for field in ("repo_id", "revision", "architecture"):
        if not isinstance(source.get(field), str) or not source[field]:
            raise ValueError(f"bundle source {field} is missing")
    module_name = engine.get("name")
    if not isinstance(module_name, str) or not module_name:
        raise ValueError("engine name is missing")
    model_id = target_model_id(module_name, Path(engine["file"]).name)
    model_ids = {module_name: model_id}
    full_package_key = package_key(package_base_key, model_ids)
    metadata = {
        "version": METADATA_VERSION,
        "model_name": source["repo_id"].rsplit("/", 1)[-1],
        "config": {
            "architecture": source["architecture"],
            "package_base_key": package_base_key,
            "package_key": full_package_key,
            "repo_id": source["repo_id"],
            "result_id": RESULT_ID,
            "revision": source["revision"],
            "source_target_model_ids": model_ids,
            "ztc_target": TARGET,
        },
        "modules": [
            {
                "chunk_id": 0,
                "compatibility": COMPATIBILITY,
                "file_name": Path(engine["file"]).name,
                "io": {
                    kind: [
                        _tensor_metadata(tensor)
                        for tensor in engine["bindings"][kind]
                    ]
                    for kind in ("inputs", "outputs")
                },
                "module_name": module_name,
                "quant_type": "FP16",
                "target": TARGET,
                "target_model_id": model_id,
            }
        ],
    }
    return metadata, full_package_key


def load_binding() -> ModuleType:
    native_python_path = os.environ.get("ZETIC_MLANGE_ZTC_PYTHONPATH")
    if native_python_path and native_python_path not in sys.path:
        sys.path.insert(0, native_python_path)
    try:
        return importlib.import_module("mlange_ztc")
    except ImportError as exc:
        raise RuntimeError(
            "Zetic's native mlange_ztc binding is not installed for this platform; "
            "use the supported packaging environment or set "
            "ZETIC_MLANGE_ZTC_PYTHONPATH to its Python package directory. "
            f"mlange_ztc: {exc}"
        ) from exc


def _write_private_json(path: Path, payload: dict[str, Any]) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(payload, stream, indent=2, sort_keys=True)
        stream.write("\n")


def _different_key(key: bytes) -> bytes:
    return bytes([key[0] ^ 1]) + key[1:]


def package_and_validate(
    *,
    bundle: dict[str, Any],
    engine: dict[str, Any],
    engine_path: Path,
    output_dir: Path,
    package_base_key: str,
    binding: ModuleType,
    key_bytes: bytes,
) -> PackageResult:
    if len(key_bytes) != 32:
        raise ValueError("ZTC encryption key must contain exactly 32 bytes")
    metadata, full_package_key = build_metadata(
        bundle, engine, package_base_key
    )
    output_dir.mkdir(parents=True, exist_ok=False)
    ztc_path = output_dir / f"{full_package_key}.ztc"
    metadata_path = output_dir / f"{full_package_key}.metadata.json"
    private_path = output_dir / f"{full_package_key}.private.json"
    report_path = output_dir / f"{full_package_key}.report.json"
    metadata_json = json.dumps(metadata, indent=2, sort_keys=True)

    status = binding.ZtcLoader.pack(
        str(ztc_path), metadata_json, {0: str(engine_path)}, key_bytes
    )
    if status != binding.ZtcStatus.SUCCESS:
        ztc_path.unlink(missing_ok=True)
        raise RuntimeError(f"native ZTC pack failed: {status}")
    if not ztc_path.is_file() or ztc_path.stat().st_size == 0:
        raise RuntimeError("native ZTC pack succeeded without a non-empty file")
    metadata_path.write_text(metadata_json + "\n", encoding="utf-8")
    _write_private_json(
        private_path,
        {
            "schema": "zetic.ztc_private_handoff.v1",
            "package_key": full_package_key,
            "ztc_path": str(ztc_path.resolve()),
            "metadata_path": str(metadata_path.resolve()),
            "secret_key": key_bytes.hex(),
        },
    )

    loader = binding.ZtcLoader(str(ztc_path))
    loader.set_key(key_bytes)
    if loader.open() != binding.ZtcStatus.SUCCESS:
        raise RuntimeError("native ZTC loader could not reopen the package")
    packed_metadata = json.loads(loader.get_metadata_json())
    if packed_metadata != metadata:
        raise ValueError("metadata changed during ZTC round-trip")

    wrong_loader = binding.ZtcLoader(str(ztc_path))
    wrong_loader.set_key(_different_key(key_bytes))
    wrong_key_status = wrong_loader.open()
    if wrong_key_status != binding.ZtcStatus.ERROR_DECRYPTION_FAIL:
        raise RuntimeError(
            "wrong-key ZTC open must fail with ERROR_DECRYPTION_FAIL; "
            f"got {wrong_key_status}"
        )

    with tempfile.TemporaryDirectory(prefix=".ztc-roundtrip-", dir=output_dir) as temp:
        restored = Path(temp) / engine_path.name
        if loader.save_chunk(0, str(restored)) != binding.ZtcStatus.SUCCESS:
            raise RuntimeError("engine extraction from ZTC failed")
        restored_hash = sha256(restored)
        restored_size = restored.stat().st_size
    if restored_hash != engine["sha256"]:
        raise ValueError("engine bytes changed during ZTC round-trip")

    report = {
        "schema": "zetic.ztc_package_validation.v1",
        "status": "passed",
        "scope": "single engine package metadata and byte round-trip only",
        "package_key": full_package_key,
        "engine": {
            "name": engine["name"],
            "source_sha256": engine["sha256"],
            "restored_sha256": restored_hash,
            "restored_size": restored_size,
        },
        "ztc": {
            "file": str(ztc_path.resolve()),
            "sha256": sha256(ztc_path),
            "size": ztc_path.stat().st_size,
            "metadata_version": packed_metadata["version"],
            "module_count": len(packed_metadata["modules"]),
        },
        "checks": {
            "container_open": "passed",
            "wrong_key_rejected": "passed",
            "single_module": "passed",
            "source_identity": "passed",
            "target_compatibility": "passed",
            "binding_metadata": "passed",
            "engine_byte_roundtrip": "passed",
        },
        "excluded": [
            "TensorRT execution from the ZTC container",
            "multi-engine orchestration",
            "end-to-end model inference",
            "upload or backend registration",
        ],
    }
    report_path.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    return PackageResult(ztc_path, metadata_path, private_path, report_path)


def default_validator() -> Path:
    skills_dir = Path(__file__).resolve().parents[2]
    return skills_dir / "hf-to-tensorrt" / "scripts" / "validate_bundle.py"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--package-base-key", required=True)
    parser.add_argument("--bundle-validator", type=Path, default=default_validator())
    args = parser.parse_args()
    manifest = args.manifest.resolve()
    run_bundle_validator(manifest, args.bundle_validator.resolve())
    bundle = json.loads(manifest.read_text(encoding="utf-8"))
    engine, engine_path = select_engine(bundle, manifest.parent)
    result = package_and_validate(
        bundle=bundle,
        engine=engine,
        engine_path=engine_path,
        output_dir=args.output_dir.resolve(),
        package_base_key=args.package_base_key,
        binding=load_binding(),
        key_bytes=secrets.token_bytes(32),
    )
    print(
        json.dumps(
            {
                "ztc": str(result.ztc_path),
                "metadata": str(result.metadata_path),
                "private_manifest": str(result.private_manifest_path),
                "report": str(result.report_path),
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
