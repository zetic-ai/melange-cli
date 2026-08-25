from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
PROFILE_PATH = (
    Path(__file__).resolve().parents[1]
    / "references"
    / "supported-thor-profile.json"
)
sys.path.insert(0, str(SCRIPTS_DIR))

import check_thor_env  # noqa: E402


class CheckThorEnvTest(unittest.TestCase):
    def test_published_profile_has_complete_fingerprint(self) -> None:
        import json

        profile = json.loads(PROFILE_PATH.read_text(encoding="utf-8"))

        self.assertEqual(profile["profile"], "zetic-thor-v1")
        self.assertEqual(
            set(profile["fingerprint"]),
            {
                "gpu_name",
                "compute_capability",
                "jetpack_version",
                "tensorrt_version",
                "cuda_version",
                "driver_version",
                "os_release",
                "trtexec_sha256",
            },
        )

    def test_reads_cuda_identity_without_torch(self) -> None:
        def run(command: list[str]) -> str | None:
            if "--query-gpu=name,compute_cap" in command:
                return "NVIDIA Thor, 11.0"
            if command == ["nvidia-smi"]:
                return "Driver Version: 580.00 CUDA Version: 13.0"
            return None

        with patch.object(check_thor_env, "_run", side_effect=run):
            identity = check_thor_env._nvidia_smi_cuda()

        self.assertEqual(identity, ("NVIDIA Thor", "11.0", "13.0"))

    def test_reads_exact_tensorrt_package_version(self) -> None:
        with patch.object(
            check_thor_env,
            "_run",
            return_value="10.13.3.9-1+cuda13.0",
        ):
            version = check_thor_env._tensorrt_version(Path("trtexec"))

        self.assertEqual(version, "10.13.3.9")

    def test_parses_compact_trtexec_banner_as_fallback(self) -> None:
        def run(command: list[str]) -> str | None:
            if command[0] == "dpkg-query":
                return None
            return "TensorRT.trtexec [TensorRT v101303]"

        with patch.object(check_thor_env, "_run", side_effect=run):
            version = check_thor_env._tensorrt_version(Path("trtexec"))

        self.assertEqual(version, "10.13.3")

    def test_identical_profiles_match(self) -> None:
        target = {
            "profile": "zetic-thor-v1",
            "fingerprint": {"cuda_version": "13.0", "trtexec_sha256": "a" * 64},
        }

        self.assertEqual(check_thor_env.differences(target, target), [])

    def test_reports_each_mismatch(self) -> None:
        observed = {
            "profile": "zetic-thor-v1",
            "fingerprint": {"cuda_version": "13.0", "trtexec_sha256": "a" * 64},
        }
        expected = {
            "profile": "zetic-thor-v1",
            "fingerprint": {"cuda_version": "12.0", "trtexec_sha256": "b" * 64},
        }

        errors = check_thor_env.differences(observed, expected)

        self.assertEqual(len(errors), 2)
        self.assertTrue(any("cuda_version" in error for error in errors))
        self.assertTrue(any("trtexec_sha256" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
