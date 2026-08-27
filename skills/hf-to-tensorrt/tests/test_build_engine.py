from __future__ import annotations

import sys
import unittest
from pathlib import Path

SCRIPTS_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

import build_engine  # noqa: E402


class BuildEngineTest(unittest.TestCase):
    def test_fp16_command_is_static_and_skips_benchmark(self) -> None:
        command = build_engine.build_command(
            trtexec=Path("/target/trtexec"),
            onnx_path=Path("/work/model.onnx"),
            engine_path=Path("/work/model.engine"),
            typing="fp16",
            timing_cache=None,
        )

        self.assertEqual(
            command,
            [
                "/target/trtexec",
                "--onnx=/work/model.onnx",
                "--saveEngine=/work/model.engine",
                "--fp16",
                "--skipInference",
            ],
        )

    def test_strongly_typed_command_includes_timing_cache(self) -> None:
        command = build_engine.build_command(
            trtexec=Path("trtexec"),
            onnx_path=Path("model.onnx"),
            engine_path=Path("model.engine"),
            typing="strongly_typed",
            timing_cache=Path("target.cache"),
        )

        self.assertIn("--stronglyTyped", command)
        self.assertIn("--timingCacheFile=target.cache", command)
        self.assertNotIn("--fp16", command)

    def test_unknown_typing_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "unsupported"):
            build_engine.build_command(
                trtexec=Path("trtexec"),
                onnx_path=Path("model.onnx"),
                engine_path=Path("model.engine"),
                typing="int8",
                timing_cache=None,
            )

    def test_disable_tf32_is_recorded_in_command(self) -> None:
        command = build_engine.build_command(
            trtexec=Path("trtexec"),
            onnx_path=Path("model.onnx"),
            engine_path=Path("model.engine"),
            typing="strongly_typed",
            timing_cache=None,
            disable_tf32=True,
        )

        self.assertIn("--noTF32", command)
        self.assertLess(command.index("--stronglyTyped"), command.index("--noTF32"))


if __name__ == "__main__":
    unittest.main()
