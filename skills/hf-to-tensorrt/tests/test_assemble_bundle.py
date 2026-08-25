from __future__ import annotations

import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import assemble_bundle  # noqa: E402
import validate_bundle  # noqa: E402


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _write(path: Path, payload: dict) -> None:
    path.write_text(json.dumps(payload), encoding="utf-8")


class AssembleBundleTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        self.artifact = self.root / "artifacts" / "encoder"
        self.artifact.mkdir(parents=True)
        self.engine = self.artifact / "model.engine"
        self.engine.write_bytes(b"engine")
        self.bindings = {
            "inputs": [{"name": "input", "dtype": "float32", "shape": [1, 4]}],
            "outputs": [{"name": "output", "dtype": "float16", "shape": [1, 2]}],
        }
        _write(
            self.artifact / "build.json",
            {
                "schema": "zetic.tensorrt_build.v1",
                "status": "succeeded",
                "typing": "fp16",
                "onnx_sha256": "b" * 64,
                "trtexec_command": ["trtexec", "--onnx=model.onnx", "--fp16"],
                "engine_sha256": _sha256(self.engine),
                "engine_size": self.engine.stat().st_size,
                **self.bindings,
            },
        )
        _write(
            self.artifact / "engine-metadata.json",
            {
                "schema": "zetic.tensorrt_engine.v1",
                "file": "model.engine",
                "size": self.engine.stat().st_size,
                "sha256": _sha256(self.engine),
                "tensorrt_version": "test",
                **self.bindings,
            },
        )
        _write(
            self.artifact / "parity.json",
            {
                "schema": "zetic.engine_parity.v1",
                "policy": "zetic.engine_parity.v1",
                "status": "passed",
                "fixture_count": 1,
                "reference_sha256": "c" * 64,
                "actual_sha256": "d" * 64,
                "outputs": [
                    {
                        "name": "output",
                        "reference_dtype": "float32",
                        "actual_dtype": "float16",
                        "reference_shape": [1, 2],
                        "actual_shape": [1, 2],
                        "comparison": "allclose",
                        "rtol": 0.01,
                        "atol": 0.01,
                        "element_count": 2,
                        "mismatch_count": 0,
                        "nonfinite_count": 0,
                        "max_abs_error": 0.001,
                        "max_rel_error": 0.001,
                        "passed": True,
                    }
                ],
            },
        )
        self.target = self.root / "thor-target.json"
        _write(
            self.target,
            {
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
        )
        self.output = self.root / "engine-bundle.json"

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def assemble(self) -> dict:
        return assemble_bundle.assemble(
            output=self.output,
            repo_id="owner/model",
            revision="0123456789abcdef0123456789abcdef01234567",
            architecture="ExampleModel",
            target_path=self.target,
            engine_dirs=[self.artifact],
        )

    def test_assembles_and_validates_manifest(self) -> None:
        manifest = self.assemble()

        self.assertEqual(manifest["engines"][0]["name"], "encoder")
        self.assertEqual(
            manifest["engines"][0]["file"], "artifacts/encoder/model.engine"
        )
        self.assertEqual(validate_bundle.validate(self.output), [])

    def test_rejects_onnx_engine_contract_mismatch_without_manifest(self) -> None:
        build_path = self.artifact / "build.json"
        build = json.loads(build_path.read_text(encoding="utf-8"))
        build["outputs"][0]["shape"] = [1, 3]
        _write(build_path, build)

        with self.assertRaisesRegex(ValueError, "ONNX/engine contract"):
            self.assemble()

        self.assertFalse(self.output.exists())

    def test_rejects_parity_engine_contract_mismatch(self) -> None:
        report_path = self.artifact / "parity.json"
        report = json.loads(report_path.read_text(encoding="utf-8"))
        report["outputs"][0]["actual_dtype"] = "float32"
        _write(report_path, report)

        with self.assertRaisesRegex(ValueError, "parity dtype"):
            self.assemble()

    def test_refuses_to_overwrite_manifest(self) -> None:
        self.output.write_text("keep", encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "overwrite"):
            self.assemble()

        self.assertEqual(self.output.read_text(encoding="utf-8"), "keep")


if __name__ == "__main__":
    unittest.main()
