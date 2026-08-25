from __future__ import annotations

import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import validate_bundle  # noqa: E402


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class ValidateBundleTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        artifact_dir = self.root / "artifacts" / "encoder"
        artifact_dir.mkdir(parents=True)
        self.engine = artifact_dir / "model.engine"
        self.engine.write_bytes(b"fake-tensorrt-engine")
        self.report = artifact_dir / "parity.json"
        self.report.write_text(
            json.dumps(
                {
                    "schema": "zetic.engine_parity.v1",
                    "policy": "zetic.engine_parity.v1",
                    "status": "passed",
                    "fixture_count": 1,
                    "reference_sha256": "d" * 64,
                    "actual_sha256": "e" * 64,
                    "outputs": [
                        {
                            "name": "output",
                            "reference_dtype": "float32",
                            "actual_dtype": "float16",
                            "reference_shape": [1, 4],
                            "actual_shape": [1, 4],
                            "comparison": "allclose",
                            "rtol": 0.01,
                            "atol": 0.01,
                            "element_count": 4,
                            "mismatch_count": 0,
                            "nonfinite_count": 0,
                            "max_abs_error": 0.001,
                            "max_rel_error": 0.001,
                            "passed": True,
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        self.manifest = self.root / "engine-bundle.json"

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def payload(self) -> dict:
        return {
            "schema": "zetic.engine_bundle.v1",
            "source": {
                "repo_id": "owner/model",
                "revision": "0123456789abcdef0123456789abcdef01234567",
                "architecture": "ExampleModel",
            },
            "target": {
                "profile": "zetic-thor-v1",
                "fingerprint": {
                    "gpu_name": "NVIDIA Thor",
                    "compute_capability": "10.0",
                    "jetpack_version": "test",
                    "tensorrt_version": "test",
                    "cuda_version": "test",
                    "driver_version": "test",
                    "os_release": "test",
                    "trtexec_sha256": "a" * 64,
                },
            },
            "engines": [
                {
                    "name": "encoder",
                    "file": "artifacts/encoder/model.engine",
                    "sha256": _sha256(self.engine),
                    "precision": "fp16",
                    "bindings": {
                        "inputs": [
                            {"name": "input", "dtype": "float32", "shape": [1, 4]}
                        ],
                        "outputs": [
                            {"name": "output", "dtype": "float16", "shape": [1, 4]}
                        ],
                    },
                    "build": {
                        "onnx_sha256": "b" * 64,
                        "trtexec_command": ["trtexec", "--onnx=model.onnx"],
                    },
                    "parity": {
                        "policy": "zetic.engine_parity.v1",
                        "status": "passed",
                        "fixture_count": 1,
                        "report_file": "artifacts/encoder/parity.json",
                        "report_sha256": _sha256(self.report),
                    },
                }
            ],
        }

    def write(self, payload: dict) -> None:
        self.manifest.write_text(json.dumps(payload), encoding="utf-8")

    def test_accepts_valid_bundle(self) -> None:
        self.write(self.payload())

        self.assertEqual(validate_bundle.validate(self.manifest), [])

    def test_rejects_engine_hash_mismatch(self) -> None:
        payload = self.payload()
        payload["engines"][0]["sha256"] = "c" * 64
        self.write(payload)

        errors = validate_bundle.validate(self.manifest)

        self.assertTrue(any("SHA-256 mismatch" in error for error in errors))

    def test_rejects_duplicate_binding_name(self) -> None:
        payload = self.payload()
        payload["engines"][0]["bindings"]["outputs"][0]["name"] = "input"
        self.write(payload)

        errors = validate_bundle.validate(self.manifest)

        self.assertTrue(any("duplicates binding name" in error for error in errors))

    def test_rejects_orchestration_metadata(self) -> None:
        payload = self.payload()
        payload["orchestration"] = {"order": ["encoder"]}
        self.write(payload)

        errors = validate_bundle.validate(self.manifest)

        self.assertTrue(any("Additional properties" in error for error in errors))

    def test_rejects_parent_path(self) -> None:
        payload = self.payload()
        payload["engines"][0]["file"] = "../model.engine"
        self.write(payload)

        errors = validate_bundle.validate(self.manifest)

        self.assertTrue(any("does not match" in error for error in errors))

    def test_rejects_failed_parity_report(self) -> None:
        report = json.loads(self.report.read_text(encoding="utf-8"))
        report["status"] = "failed"
        report["outputs"][0]["passed"] = False
        report["outputs"][0]["mismatch_count"] = 1
        self.report.write_text(json.dumps(report), encoding="utf-8")
        payload = self.payload()
        payload["engines"][0]["parity"]["report_sha256"] = _sha256(self.report)
        self.write(payload)

        errors = validate_bundle.validate(self.manifest)

        self.assertTrue(any("status is not passed" in error for error in errors))

    def test_rejects_internally_inconsistent_passing_report(self) -> None:
        report = json.loads(self.report.read_text(encoding="utf-8"))
        report["outputs"][0]["passed"] = False
        report["outputs"][0]["mismatch_count"] = 1
        self.report.write_text(json.dumps(report), encoding="utf-8")
        payload = self.payload()
        payload["engines"][0]["parity"]["report_sha256"] = _sha256(self.report)
        self.write(payload)

        errors = validate_bundle.validate(self.manifest)

        self.assertTrue(any("output 'output' did not pass" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
