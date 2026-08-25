# SPDX-License-Identifier: Apache-2.0
"""Standalone LFM2.5-VL static-engine decomposition example.

This file intentionally has no Mentat or Melange imports. It demonstrates how a
multimodal Hugging Face model can be reshaped into four independently exportable
tensor-to-tensor modules:

* one fixed-slot vision tile encoder;
* one per-tile projector MLP;
* one fixed-length language prefill;
* one fixed-window, single-token language decode.

Image tiling, position-embedding resize, pixel unshuffle, token embedding, and
generation loops stay outside the engines. The helper functions below define the
corresponding host-side reference math, not an end-to-end runtime.

This is a worked decomposition example, not a universal exporter. A production
conversion must capture real intermediate tensors from the user's inference path
and prove parity at each chosen boundary.
"""

from __future__ import annotations

import argparse
import inspect
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import onnx
import torch
import torch.nn as nn
import torch.nn.functional as F


REPO_ID = "LiquidAI/LFM2.5-VL-450M"
MASK_VALUE = -65504.0


def _field(config: Any, name: str) -> Any:
    if isinstance(config, dict) and name in config:
        return config[name]
    if hasattr(config, name):
        return getattr(config, name)
    raise ValueError(f"LFM configuration is missing {name!r}")


def _optional(config: Any, name: str, default: Any = None) -> Any:
    if isinstance(config, dict):
        return config.get(name, default)
    return getattr(config, name, default)


@dataclass(frozen=True)
class LFMConfig:
    layer_types: list[str]
    num_layers: int
    hidden_size: int
    num_heads: int
    num_kv_heads: int
    head_dim: int
    num_kv_groups: int
    conv_l_cache: int
    vocab_size: int
    rope_theta: float
    norm_eps: float
    conv_layer_ids: list[int]
    attn_layer_ids: list[int]

    @classmethod
    def from_hf_config(cls, config: Any) -> "LFMConfig":
        text_config = _optional(config, "text_config")
        decoder = text_config if text_config is not None else config
        layer_types = list(_field(decoder, "layer_types"))
        hidden_size = int(_field(decoder, "hidden_size"))
        num_heads = int(_field(decoder, "num_attention_heads"))
        num_kv_heads = int(_field(decoder, "num_key_value_heads"))
        rope_theta = _optional(decoder, "rope_theta")
        if rope_theta is None:
            rope_theta = (_optional(decoder, "rope_parameters", {}) or {}).get(
                "rope_theta"
            )
        if rope_theta is None:
            raise ValueError("LFM config has no rope_theta")
        return cls(
            layer_types=layer_types,
            num_layers=int(_field(decoder, "num_hidden_layers")),
            hidden_size=hidden_size,
            num_heads=num_heads,
            num_kv_heads=num_kv_heads,
            head_dim=hidden_size // num_heads,
            num_kv_groups=num_heads // num_kv_heads,
            conv_l_cache=int(_field(decoder, "conv_L_cache")),
            vocab_size=int(_field(decoder, "vocab_size")),
            rope_theta=float(rope_theta),
            norm_eps=float(_field(decoder, "norm_eps")),
            conv_layer_ids=[
                index for index, kind in enumerate(layer_types) if kind == "conv"
            ],
            attn_layer_ids=[
                index
                for index, kind in enumerate(layer_types)
                if kind == "full_attention"
            ],
        )


@dataclass(frozen=True)
class ExportModule:
    name: str
    module: nn.Module
    sample_inputs: list[tuple[str, torch.Tensor]]
    output_names: list[str]


def _vision_transformer(tower: nn.Module) -> nn.Module:
    return tower if hasattr(tower, "embeddings") else tower.vision_model


def _language_decoder_and_head(model: nn.Module) -> tuple[nn.Module, nn.Module]:
    inner = model.model
    decoder = inner.language_model if hasattr(inner, "language_model") else inner
    return decoder, model.lm_head


def rms_norm(x: torch.Tensor, weight: torch.Tensor, eps: float) -> torch.Tensor:
    variance = x.pow(2).mean(-1, keepdim=True)
    return x * torch.rsqrt(variance + eps) * weight


def rotate_half(x: torch.Tensor) -> torch.Tensor:
    midpoint = x.shape[-1] // 2
    return torch.cat((-x[..., midpoint:], x[..., :midpoint]), dim=-1)


def compute_rope(
    positions: torch.Tensor,
    head_dim: int,
    rope_theta: float,
    device: torch.device,
) -> tuple[torch.Tensor, torch.Tensor]:
    inv_freq = 1.0 / (
        rope_theta
        ** (torch.arange(0, head_dim, 2, dtype=torch.float32, device=device) / head_dim)
    )
    frequencies = positions.float().squeeze(0).unsqueeze(-1) * inv_freq.unsqueeze(0)
    embedding = torch.cat([frequencies, frequencies], dim=-1)
    return embedding.cos().unsqueeze(0), embedding.sin().unsqueeze(0)


def apply_rope(
    query: torch.Tensor,
    key: torch.Tensor,
    cosine: torch.Tensor,
    sine: torch.Tensor,
) -> tuple[torch.Tensor, torch.Tensor]:
    cosine = cosine.unsqueeze(1)
    sine = sine.unsqueeze(1)
    return (
        query * cosine + rotate_half(query) * sine,
        key * cosine + rotate_half(key) * sine,
    )


def repeat_kv(x: torch.Tensor, repetitions: int) -> torch.Tensor:
    if repetitions == 1:
        return x
    return (
        x.unsqueeze(2)
        .expand(-1, -1, repetitions, -1, -1)
        .reshape(x.shape[0], -1, x.shape[2], x.shape[3])
    )


def ffn_forward(layer: nn.Module, hidden: torch.Tensor, cfg: LFMConfig) -> torch.Tensor:
    normalized = rms_norm(hidden, layer.ffn_norm.weight, cfg.norm_eps)
    feed_forward = layer.feed_forward
    return hidden + feed_forward.w2(
        F.silu(feed_forward.w1(normalized)) * feed_forward.w3(normalized)
    )


def conv_decode(
    layer: nn.Module,
    hidden: torch.Tensor,
    state: torch.Tensor,
    cfg: LFMConfig,
) -> tuple[torch.Tensor, torch.Tensor]:
    normalized = rms_norm(hidden, layer.operator_norm.weight, cfg.norm_eps)
    bcx = layer.conv.in_proj(normalized).transpose(-1, -2)
    b, c, x = bcx.chunk(3, dim=-2)
    bx = b * x
    new_state = torch.cat([state[:, :, 1:], bx], dim=-1)
    weight = layer.conv.conv.weight[:, 0, :]
    convolved = torch.sum(new_state * weight.unsqueeze(0), dim=-1).unsqueeze(-1)
    output = layer.conv.out_proj((c * convolved).transpose(-1, -2).contiguous())
    return output, new_state


class VisionTileEncoder(nn.Module):
    """Encode one fixed-size padded tile.

    Resized position embeddings and the additive validity mask are explicit
    inputs. Their values depend on the source image layout, so constructing them
    inside this engine would make the graph input-dependent.
    """

    def __init__(self, tower: nn.Module):
        super().__init__()
        self.tower = _vision_transformer(tower)
        self.tower.config._attn_implementation = "eager"

    def forward(
        self,
        pixel_values: torch.Tensor,
        position_embeddings: torch.Tensor,
        attention_bias: torch.Tensor,
    ) -> torch.Tensor:
        embeddings = self.tower.embeddings
        patch_dtype = embeddings.patch_embedding.weight.dtype
        hidden = embeddings.patch_embedding(pixel_values.to(patch_dtype))
        hidden = hidden + position_embeddings.to(hidden.dtype)
        encoded = self.tower.encoder(
            inputs_embeds=hidden, attention_mask=attention_bias
        ).last_hidden_state
        return self.tower.post_layernorm(encoded)


def host_position_embeddings(
    tower: nn.Module,
    spatial_shapes: torch.Tensor,
    max_patches: int,
) -> torch.Tensor:
    """Reference host operation intentionally excluded from VisionTileEncoder."""
    embeddings = _vision_transformer(tower).embeddings
    base = embeddings.position_embedding.weight.reshape(
        embeddings.position_embedding_size,
        embeddings.position_embedding_size,
        -1,
    )
    return embeddings.resize_positional_embeddings(
        base, spatial_shapes, max_length=max_patches
    )


def host_attention_bias(
    pixel_attention_mask: torch.Tensor,
) -> torch.Tensor:
    """Convert per-patch validity to a fixed additive attention tensor."""
    mask = pixel_attention_mask.to(torch.float32)
    bias = torch.where(
        mask == 1,
        torch.zeros_like(mask),
        torch.full_like(mask, MASK_VALUE),
    )
    return bias[:, None, None, :]


class VisionProjector(nn.Module):
    """Project one fixed-size tile after host-side pixel unshuffle."""

    def __init__(self, projector: nn.Module):
        super().__init__()
        self.projector = projector

    def forward(self, unshuffled_patches: torch.Tensor) -> torch.Tensor:
        projector = self.projector
        hidden = unshuffled_patches
        if projector.use_layer_norm:
            hidden = projector.layer_norm(hidden)
        hidden = projector.linear_1(hidden)
        hidden = projector.act(hidden)
        return projector.linear_2(hidden)


def host_pixel_unshuffle(
    projector: nn.Module,
    tile_hidden: torch.Tensor,
    height: int,
    width: int,
) -> torch.Tensor:
    """Reference shape-dependent reshape intentionally excluded from the engine."""
    feature = tile_hidden.reshape(1, height, width, -1)
    result = projector.pixel_unshuffle(feature)
    return result.reshape(-1, result.shape[-1])


class LanguagePrefill(nn.Module):
    """Fixed-length left-padded prefill with explicit output state."""

    def __init__(self, model: nn.Module, cfg: LFMConfig):
        super().__init__()
        self.decoder, self.lm_head = _language_decoder_and_head(model)
        self.cfg = cfg

    def forward(self, input_embeds: torch.Tensor, token_offset: torch.Tensor):
        cfg = self.cfg
        sequence_length = input_embeds.shape[1]
        hidden = input_embeds
        positions = torch.arange(sequence_length, device=input_embeds.device)
        position_ids = torch.clamp(positions - token_offset, min=0)
        cosine, sine = compute_rope(
            position_ids.unsqueeze(0), cfg.head_dim, cfg.rope_theta, hidden.device
        )

        row = positions.unsqueeze(1)
        column = positions.unsqueeze(0)
        causal = (column <= row).float()
        valid_column = (column >= token_offset).float()
        bias = ((1.0 - causal * valid_column) * MASK_VALUE)[None, None, :, :]
        valid_row = (positions >= token_offset).float().view(1, sequence_length, 1)

        conv_states: list[torch.Tensor] = []
        kv_pairs: list[torch.Tensor] = []
        for layer_index, kind in enumerate(cfg.layer_types):
            layer = self.decoder.layers[layer_index]
            residual = hidden
            if kind == "conv":
                normalized = rms_norm(hidden, layer.operator_norm.weight, cfg.norm_eps)
                bcx = layer.conv.in_proj(normalized).transpose(-1, -2)
                b, c, x = bcx.chunk(3, dim=-2)
                bx = b * x
                padded = F.pad(bx, (cfg.conv_l_cache - 1, 0))
                convolved = F.conv1d(
                    padded, layer.conv.conv.weight, groups=cfg.hidden_size
                )
                output = layer.conv.out_proj(
                    (c * convolved).transpose(-1, -2).contiguous()
                )
                conv_states.append(bx[:, :, -cfg.conv_l_cache :])
            else:
                normalized = rms_norm(hidden, layer.operator_norm.weight, cfg.norm_eps)
                attention = layer.self_attn
                query = attention.q_proj(normalized).view(
                    1, sequence_length, cfg.num_heads, cfg.head_dim
                ).transpose(1, 2)
                key = attention.k_proj(normalized).view(
                    1, sequence_length, cfg.num_kv_heads, cfg.head_dim
                ).transpose(1, 2)
                value = attention.v_proj(normalized).view(
                    1, sequence_length, cfg.num_kv_heads, cfg.head_dim
                ).transpose(1, 2)
                query = rms_norm(query, attention.q_layernorm.weight, cfg.norm_eps)
                key = rms_norm(key, attention.k_layernorm.weight, cfg.norm_eps)
                query, key = apply_rope(query, key, cosine, sine)
                repeated_key = repeat_kv(key, cfg.num_kv_groups)
                repeated_value = repeat_kv(value, cfg.num_kv_groups)
                scores = torch.matmul(query, repeated_key.transpose(2, 3))
                scores = scores * (cfg.head_dim**-0.5) + bias
                weights = F.softmax(scores, dim=-1, dtype=torch.float32)
                attended = torch.matmul(weights, repeated_value)
                attended = attended.transpose(1, 2).reshape(
                    1, sequence_length, cfg.hidden_size
                )
                output = attention.out_proj(attended) * valid_row
                kv_pairs.extend([key, value])
            hidden = ffn_forward(layer, output + residual, cfg)

        hidden = rms_norm(hidden, self.decoder.embedding_norm.weight, cfg.norm_eps)
        logits = self.lm_head(hidden[:, -1:, :]).squeeze(1)
        return (logits, *conv_states, *kv_pairs)


class LanguageDecode(nn.Module):
    """Single-token decode with explicit fixed-window convolution and KV state."""

    def __init__(self, model: nn.Module, cfg: LFMConfig):
        super().__init__()
        self.decoder, self.lm_head = _language_decoder_and_head(model)
        self.cfg = cfg

    def forward(
        self,
        input_embed: torch.Tensor,
        cache_position: torch.Tensor,
        attention_mask: torch.Tensor,
        *state_tensors: torch.Tensor,
    ):
        cfg = self.cfg
        conv_count = len(cfg.conv_layer_ids)
        input_conv_states = list(state_tensors[:conv_count])
        input_kv_pairs = state_tensors[conv_count:]
        bias = ((1.0 - attention_mask) * MASK_VALUE).view(1, 1, 1, -1)
        hidden = input_embed
        cosine, sine = compute_rope(
            cache_position.unsqueeze(0), cfg.head_dim, cfg.rope_theta, hidden.device
        )

        output_conv_states: list[torch.Tensor] = []
        output_kv_pairs: list[torch.Tensor] = []
        conv_index = 0
        attention_index = 0
        for layer_index, kind in enumerate(cfg.layer_types):
            layer = self.decoder.layers[layer_index]
            residual = hidden
            if kind == "conv":
                output, state = conv_decode(
                    layer, hidden, input_conv_states[conv_index], cfg
                )
                output_conv_states.append(state)
                conv_index += 1
            else:
                normalized = rms_norm(hidden, layer.operator_norm.weight, cfg.norm_eps)
                attention = layer.self_attn
                query = attention.q_proj(normalized).view(
                    1, 1, cfg.num_heads, cfg.head_dim
                ).transpose(1, 2)
                key = attention.k_proj(normalized).view(
                    1, 1, cfg.num_kv_heads, cfg.head_dim
                ).transpose(1, 2)
                value = attention.v_proj(normalized).view(
                    1, 1, cfg.num_kv_heads, cfg.head_dim
                ).transpose(1, 2)
                query = rms_norm(query, attention.q_layernorm.weight, cfg.norm_eps)
                key = rms_norm(key, attention.k_layernorm.weight, cfg.norm_eps)
                query, key = apply_rope(query, key, cosine, sine)

                past_key = input_kv_pairs[attention_index * 2]
                past_value = input_kv_pairs[attention_index * 2 + 1]
                full_key = torch.cat([past_key[:, :, 1:, :], key], dim=2)
                full_value = torch.cat([past_value[:, :, 1:, :], value], dim=2)
                repeated_key = repeat_kv(full_key, cfg.num_kv_groups)
                repeated_value = repeat_kv(full_value, cfg.num_kv_groups)
                scores = (
                    torch.matmul(query, repeated_key.transpose(2, 3))
                    * (cfg.head_dim**-0.5)
                    + bias
                )
                weights = F.softmax(scores, dim=-1, dtype=torch.float32)
                attended = torch.matmul(weights, repeated_value)
                output = attention.out_proj(
                    attended.transpose(1, 2).reshape(1, 1, cfg.hidden_size)
                )
                output_kv_pairs.extend([full_key, full_value])
                attention_index += 1
            hidden = ffn_forward(layer, output + residual, cfg)

        hidden = rms_norm(hidden, self.decoder.embedding_norm.weight, cfg.norm_eps)
        logits = self.lm_head(hidden[:, -1, :])
        return (logits, *output_conv_states, *output_kv_pairs)


def prefill_output_names(cfg: LFMConfig) -> list[str]:
    names = ["logits"]
    for layer_id in cfg.conv_layer_ids:
        names.append(f"conv_state_{layer_id}")
    for layer_id in cfg.attn_layer_ids:
        names.extend([f"key_{layer_id}", f"value_{layer_id}"])
    return names


def decode_input_names(cfg: LFMConfig) -> list[str]:
    names = ["input_embed", "cache_position", "attention_mask"]
    for layer_id in cfg.conv_layer_ids:
        names.append(f"conv_state_{layer_id}")
    for layer_id in cfg.attn_layer_ids:
        names.extend([f"past_key_{layer_id}", f"past_value_{layer_id}"])
    return names


def decode_output_names(cfg: LFMConfig) -> list[str]:
    names = ["logits"]
    for layer_id in cfg.conv_layer_ids:
        names.append(f"new_conv_state_{layer_id}")
    for layer_id in cfg.attn_layer_ids:
        names.extend([f"new_key_{layer_id}", f"new_value_{layer_id}"])
    return names


def build_modules(
    model: nn.Module,
    *,
    sequence_length: int,
    decode_window: int,
    max_patches: int,
) -> list[ExportModule]:
    cfg = LFMConfig.from_hf_config(model.config)
    vision_tower = model.model.vision_tower
    vision = _vision_transformer(vision_tower)
    projector = model.model.multi_modal_projector
    vision_hidden = vision.embeddings.position_embedding.embedding_dim
    projector_tokens = max_patches // (projector.factor**2)
    projector_channels = projector.linear_1.in_features

    decode_inputs: list[tuple[str, torch.Tensor]] = [
        ("input_embed", torch.randn(1, 1, cfg.hidden_size)),
        ("cache_position", torch.tensor([0], dtype=torch.long)),
        ("attention_mask", torch.ones(1, decode_window)),
    ]
    for layer_id in cfg.conv_layer_ids:
        decode_inputs.append(
            (
                f"conv_state_{layer_id}",
                torch.randn(1, cfg.hidden_size, cfg.conv_l_cache),
            )
        )
    for layer_id in cfg.attn_layer_ids:
        decode_inputs.extend(
            [
                (
                    f"past_key_{layer_id}",
                    torch.randn(1, cfg.num_kv_heads, decode_window, cfg.head_dim),
                ),
                (
                    f"past_value_{layer_id}",
                    torch.randn(1, cfg.num_kv_heads, decode_window, cfg.head_dim),
                ),
            ]
        )

    return [
        ExportModule(
            name="vision_encoder",
            module=VisionTileEncoder(vision_tower).eval(),
            sample_inputs=[
                ("pixel_values", torch.randn(1, max_patches, 768)),
                (
                    "position_embeddings",
                    torch.randn(1, max_patches, vision_hidden),
                ),
                ("attention_bias", torch.zeros(1, 1, 1, max_patches)),
            ],
            output_names=["last_hidden_state"],
        ),
        ExportModule(
            name="vision_projector",
            module=VisionProjector(projector).eval(),
            sample_inputs=[
                (
                    "unshuffled_patches",
                    torch.randn(1, projector_tokens, projector_channels),
                )
            ],
            output_names=["tile_features"],
        ),
        ExportModule(
            name="prefill",
            module=LanguagePrefill(model, cfg).eval(),
            sample_inputs=[
                (
                    "input_embeds",
                    torch.randn(1, sequence_length, cfg.hidden_size),
                ),
                ("token_offset", torch.tensor([0], dtype=torch.long)),
            ],
            output_names=prefill_output_names(cfg),
        ),
        ExportModule(
            name="decode",
            module=LanguageDecode(model, cfg).eval(),
            sample_inputs=decode_inputs,
            output_names=decode_output_names(cfg),
        ),
    ]


def export_onnx(spec: ExportModule, output: Path) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    parameters = inspect.signature(torch.onnx.export).parameters
    options: dict[str, Any] = {
        "input_names": [name for name, _ in spec.sample_inputs],
        "output_names": spec.output_names,
        "opset_version": 18,
        "export_params": True,
        "do_constant_folding": True,
    }
    if "external_data" in parameters:
        options["external_data"] = True
    if "dynamo" in parameters:
        options["dynamo"] = False
    with torch.no_grad():
        torch.onnx.export(
            spec.module,
            args=tuple(tensor for _, tensor in spec.sample_inputs),
            f=str(output),
            **options,
        )
    onnx.checker.check_model(str(output))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-id", default=REPO_ID)
    parser.add_argument("--revision", required=True, help="immutable HF commit SHA")
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--sequence-length", type=int, default=2048)
    parser.add_argument("--decode-window", type=int, default=2048)
    parser.add_argument("--max-patches", type=int, default=1024)
    args = parser.parse_args()

    from transformers import AutoModelForImageTextToText

    model = AutoModelForImageTextToText.from_pretrained(
        args.repo_id,
        revision=args.revision,
        trust_remote_code=True,
        dtype=torch.float32,
    ).eval()
    modules = build_modules(
        model,
        sequence_length=args.sequence_length,
        decode_window=args.decode_window,
        max_patches=args.max_patches,
    )
    contracts = []
    for spec in modules:
        output = args.output_dir / spec.name / f"{spec.name}.onnx"
        export_onnx(spec, output)
        contracts.append(
            {
                "name": spec.name,
                "onnx": output.relative_to(args.output_dir).as_posix(),
                "inputs": [
                    {
                        "name": name,
                        "dtype": str(tensor.dtype).removeprefix("torch."),
                        "shape": list(tensor.shape),
                    }
                    for name, tensor in spec.sample_inputs
                ],
                "outputs": spec.output_names,
            }
        )
    (args.output_dir / "module-contracts.json").write_text(
        json.dumps(contracts, indent=2) + "\n", encoding="utf-8"
    )


if __name__ == "__main__":
    main()
