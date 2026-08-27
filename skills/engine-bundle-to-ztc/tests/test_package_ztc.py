from __future__ import annotations

import importlib
import importlib.util
import json
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "package_ztc.py"
SPEC = importlib.util.spec_from_file_location("package_ztc", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
package_ztc = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = package_ztc
SPEC.loader.exec_module(package_ztc)


class FakeStatus:
    SUCCESS = "success"
    ERROR_DECRYPTION_FAIL = "decrypt-failed"
    ERROR_FILE_OPEN = "file-open-failed"


class FakeLoader:
    records: dict[str, dict] = {}
    wrong_key_status = FakeStatus.ERROR_DECRYPTION_FAIL

    def __init__(self, path: str):
        self.path = path
        self.key: bytes | None = None

    @staticmethod
    def pack(
        output_path: str, metadata_json: str, chunks: dict[int, str], key: bytes
    ) -> str:
        payload = Path(chunks[0]).read_bytes()
        Path(output_path).write_bytes(b"fake-ztc:" + payload)
        FakeLoader.records[output_path] = {
            "metadata": metadata_json,
            "chunk": payload,
            "key": key,
        }
        return FakeStatus.SUCCESS

    def set_key(self, key: bytes) -> None:
        self.key = key

    def open(self) -> str:
        record = self.records[self.path]
        return (
            FakeStatus.SUCCESS
            if self.key == record["key"]
            else self.wrong_key_status
        )

    def get_metadata_json(self) -> str:
        return self.records[self.path]["metadata"]

    def save_chunk(self, chunk_id: int, output_path: str) -> str:
        if chunk_id != 0:
            raise AssertionError("unexpected chunk")
        Path(output_path).write_bytes(self.records[self.path]["chunk"])
        return FakeStatus.SUCCESS


class FakeBinding:
    ZtcLoader = FakeLoader
    ZtcStatus = FakeStatus


def bundle(engine_path: Path) -> dict:
    return {
        "schema": "zetic.engine_bundle.v1",
        "source": {
            "repo_id": "facebook/sam2.1-hiera-small",
            "revision": "e" * 40,
            "architecture": "Sam2Model",
        },
        "target": {"profile": "zetic-thor-v1", "fingerprint": {}},
        "engines": [
            {
                "name": "image_encoder",
                "file": engine_path.name,
                "sha256": package_ztc.sha256(engine_path),
                "precision": "strongly_typed",
                "bindings": {
                    "inputs": [
                        {
                            "name": "pixel_values",
                            "dtype": "float32",
                            "shape": [1, 3, 1024, 1024],
                        }
                    ],
                    "outputs": [
                        {
                            "name": "image_embeddings",
                            "dtype": "float16",
                            "shape": [1, 256, 64, 64],
                        }
                    ],
                },
            }
        ],
    }


class PackageZtcTest(unittest.TestCase):
    def setUp(self) -> None:
        FakeLoader.records.clear()
        FakeLoader.wrong_key_status = FakeStatus.ERROR_DECRYPTION_FAIL
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.engine = self.root / "model.engine"
        self.engine.write_bytes(b"real-engine-bytes")
        self.bundle = bundle(self.engine)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def test_ids_match_the_existing_thor_package_contract(self) -> None:
        target_id = package_ztc.target_model_id("image_encoder", "model.engine")
        self.assertEqual(target_id, "e70dae05")
        self.assertEqual(
            package_ztc.package_key(
                "sam21_image_encoder", {"image_encoder": target_id}
            ),
            "sam21_image_encoder_2109ebc7",
        )

    def test_rejects_multi_engine_bundle(self) -> None:
        self.bundle["engines"].append(dict(self.bundle["engines"][0]))
        with self.assertRaisesRegex(ValueError, "exactly one engine"):
            package_ztc.select_engine(self.bundle, self.root)

    def test_rejects_engine_hash_mismatch(self) -> None:
        self.bundle["engines"][0]["sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            package_ztc.select_engine(self.bundle, self.root)

    def test_packages_and_roundtrips_without_disclosing_key(self) -> None:
        engine, engine_path = package_ztc.select_engine(self.bundle, self.root)
        output = self.root / "output"
        result = package_ztc.package_and_validate(
            bundle=self.bundle,
            engine=engine,
            engine_path=engine_path,
            output_dir=output,
            package_base_key="sam21_image_encoder",
            binding=FakeBinding,
            key_bytes=b"k" * 32,
        )

        report = json.loads(result.report_path.read_text(encoding="utf-8"))
        private = json.loads(result.private_manifest_path.read_text(encoding="utf-8"))
        metadata = json.loads(result.metadata_path.read_text(encoding="utf-8"))
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["checks"]["wrong_key_rejected"], "passed")
        self.assertEqual(report["engine"]["source_sha256"], report["engine"]["restored_sha256"])
        self.assertNotIn("secret_key", json.dumps(report))
        self.assertEqual(private["secret_key"], (b"k" * 32).hex())
        self.assertEqual(stat.S_IMODE(result.private_manifest_path.stat().st_mode), 0o600)
        self.assertEqual(metadata["modules"][0]["module_name"], "image_encoder")
        self.assertEqual(metadata["modules"][0]["io"]["inputs"][0]["name"], "pixel_values")

    def test_rejects_non_decryption_error_during_wrong_key_check(self) -> None:
        engine, engine_path = package_ztc.select_engine(self.bundle, self.root)
        FakeLoader.wrong_key_status = FakeStatus.ERROR_FILE_OPEN

        with self.assertRaisesRegex(RuntimeError, "ERROR_DECRYPTION_FAIL"):
            package_ztc.package_and_validate(
                bundle=self.bundle,
                engine=engine,
                engine_path=engine_path,
                output_dir=self.root / "output",
                package_base_key="sam21_image_encoder",
                binding=FakeBinding,
                key_bytes=b"k" * 32,
            )

    def test_binding_path_can_be_supplied_by_supported_environment(self) -> None:
        native_dir = self.root / "native-python"
        native_dir.mkdir()
        (native_dir / "mlange_ztc.py").write_text(
            "PUBLIC_BINDING = True\n", encoding="utf-8"
        )
        native_path = str(native_dir)
        try:
            sys.path.remove(native_path)
        except ValueError:
            pass
        sys.modules.pop("mlange_ztc", None)
        importlib.invalidate_caches()
        with patch.dict(
            package_ztc.os.environ,
            {"ZETIC_MLANGE_ZTC_PYTHONPATH": native_path},
        ):
            binding = package_ztc.load_binding()
            self.assertTrue(binding.PUBLIC_BINDING)
            self.assertEqual(Path(binding.__file__).parent, native_dir)
            self.assertEqual(sys.path[0], native_path)
        sys.modules.pop("mlange_ztc", None)
        sys.path.remove(native_path)


if __name__ == "__main__":
    unittest.main()
