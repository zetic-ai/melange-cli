# LFM-VL decomposition case study

This case study explains the decisions embodied in
`assets/lfm-vl-reference/lfm_vl_engine_modules.py`. The file is standalone and
contains no Mentat registry, worker, packaging, or runtime code. Copy the asset
into a scratch `uv` project when inspecting it; do not treat its model sizes or
class names as generic rules.

## Source shape

LFM2.5-VL combines variable image tiling, a SigLIP-style vision tower, a
multimodal projector, and a convolution/attention hybrid language decoder. The
user-facing inference path includes Python loops and image-dependent shape work,
while TensorRT v1 requires fully static tensor boundaries.

A monolithic export is therefore the wrong first artifact. It would mix four
different sources of dynamism:

- the number and spatial dimensions of image tiles;
- position-embedding interpolation for each tile shape;
- concatenation of a variable number of visual tokens with text;
- a language-generation loop whose KV state grows over time.

The useful cut is not “one engine per transformer layer.” It is one engine per
stable semantic computation invoked by those host loops.

## Selected boundaries

| Engine | Fixed computation | Explicit inputs | Externalized work |
| --- | --- | --- | --- |
| `vision_encoder` | Encode one padded tile | patches, resized position embeddings, additive attention bias | image tiling, PE interpolation, mask construction, tile loop |
| `vision_projector` | Apply per-token projector MLP | fixed padded unshuffled tokens | shape-dependent pixel unshuffle and valid-token slicing |
| `prefill` | Process one fixed left-padded language window | input embeddings and token offset | tokenization, embedding lookup, visual/text splice |
| `decode` | Advance one token against fixed state buffers | one embedding, position, validity mask, convolution state, KV state | token selection, embedding lookup, generation loop and stopping |

Each boundary can be invoked and compared independently from tensors captured in
the original PyTorch path. No engine needs to know how another engine is called.

## Why the vision inputs are injected

The vision tower normally derives resized position embeddings and attention
masks from each tile's spatial shape. Those operations are valid PyTorch but are
input-dependent graph construction. The adapter moves them to
`host_position_embeddings` and `host_attention_bias`, then injects fixed-shape
tensors into `VisionTileEncoder`.

This pattern generalizes when an expensive tensor computation is stable but a
small surrounding operation chooses shapes or constructs indices from Python.
Externalize the latter only if the original program can capture and reproduce
its tensor result exactly.

## Why the projector is separate

Pixel unshuffle is a reshape/permute whose output length depends on tile height
and width. The projector MLP itself is per-token and shape-stable. Performing the
unshuffle in `host_pixel_unshuffle`, padding to one fixed token slot, and exporting
only the MLP avoids coupling the engine to one image aspect ratio.

Splitting each projector layer would add bindings without eliminating any
dynamism, so it is rejected.

## Why prefill and decode differ

Prefill consumes a complete fixed window and produces the initial convolution
and attention states. Decode consumes one new embedding and fixed-size state
buffers. The decode adapter rolls each KV buffer with Slice/Concat instead of
growing it or scattering into a dynamic location. The host supplies an explicit
validity mask for the live slots.

This makes state visible in the engine contract and gives both modules static
shapes. It does not define who owns the generation loop; that remains outside
the engine artifacts and outside this skill's handoff metadata.

## Validation sequence

For a real conversion, random tensors in the example only size the ONNX graphs.
They are not parity evidence. Instrument the user's working inference path and
capture one real fixture at each selected boundary, then require:

1. source module versus standalone adapter;
2. adapter versus exported ONNX;
3. adapter versus TensorRT engine under `zetic.engine_parity.v1`.

Do not validate an LFM generation loop as part of the individual engine gate.

## What generalizes

- Cut around Python loops and shape construction, not around arbitrary layers.
- Make hidden state explicit tensor I/O.
- Replace growing state with fixed slots or windows.
- Inject small input-dependent tensors when their host calculation is exact.
- Preserve the largest independently verifiable computation at each boundary.

The exact four-engine layout, LFM layer attributes, cache format, patch size, and
sequence lengths are model-specific and must not be copied into another family
without a fresh source probe.
