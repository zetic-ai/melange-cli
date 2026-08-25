from __future__ import annotations

import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

import torch

ASSET_DIR = Path(__file__).resolve().parents[1] / "assets" / "lfm-vl-reference"
sys.path.insert(0, str(ASSET_DIR))

import lfm_vl_engine_modules as example  # noqa: E402


class FakeEmbeddings(torch.nn.Module):
    def __init__(self, hidden_size: int):
        super().__init__()
        self.position_embedding = torch.nn.Embedding(16, hidden_size)
        self.position_embedding_size = 4
        self.patch_embedding = torch.nn.Linear(768, hidden_size)


class FakeTower(torch.nn.Module):
    def __init__(self, hidden_size: int):
        super().__init__()
        self.embeddings = FakeEmbeddings(hidden_size)
        self.config = SimpleNamespace(_attn_implementation="sdpa")


class FakeProjector(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.factor = 2
        self.use_layer_norm = False
        self.layer_norm = None
        self.linear_1 = torch.nn.Linear(24, 8)
        self.act = torch.nn.GELU()
        self.linear_2 = torch.nn.Linear(8, 4)


class FakeModel(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.config = {
            "text_config": {
                "layer_types": ["conv", "full_attention"],
                "num_hidden_layers": 2,
                "hidden_size": 4,
                "num_attention_heads": 2,
                "num_key_value_heads": 1,
                "conv_L_cache": 3,
                "vocab_size": 11,
                "rope_theta": 10000.0,
                "norm_eps": 1e-5,
            }
        }
        language_model = SimpleNamespace(
            layers=[], embedding_norm=torch.nn.LayerNorm(4)
        )
        self.model = SimpleNamespace(
            language_model=language_model,
            vision_tower=FakeTower(6),
            multi_modal_projector=FakeProjector(),
        )
        self.lm_head = torch.nn.Linear(4, 11)


class LFMVLReferenceTest(unittest.TestCase):
    def test_builds_four_static_module_contracts(self) -> None:
        modules = example.build_modules(
            FakeModel(), sequence_length=8, decode_window=16, max_patches=16
        )

        self.assertEqual(
            [module.name for module in modules],
            ["vision_encoder", "vision_projector", "prefill", "decode"],
        )
        by_name = {module.name: module for module in modules}
        self.assertEqual(
            dict(by_name["vision_encoder"].sample_inputs)["pixel_values"].shape,
            (1, 16, 768),
        )
        self.assertEqual(
            dict(by_name["vision_projector"].sample_inputs)[
                "unshuffled_patches"
            ].shape,
            (1, 4, 24),
        )
        self.assertEqual(
            dict(by_name["prefill"].sample_inputs)["input_embeds"].shape,
            (1, 8, 4),
        )
        self.assertEqual(
            dict(by_name["decode"].sample_inputs)["past_key_1"].shape,
            (1, 1, 16, 2),
        )

    def test_host_attention_bias_is_explicit_and_fixed_shape(self) -> None:
        mask = torch.tensor([[1, 1, 0, 0]], dtype=torch.long)

        bias = example.host_attention_bias(mask)

        expected = torch.tensor([[[[0.0, 0.0, example.MASK_VALUE, example.MASK_VALUE]]]])
        self.assertTrue(torch.equal(bias, expected))


if __name__ == "__main__":
    unittest.main()
