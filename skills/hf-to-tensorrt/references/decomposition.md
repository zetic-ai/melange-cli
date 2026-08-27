# Static engine decomposition

The goal is the smallest number of independently verifiable static engines that
preserves the source model's meaningful reuse boundaries, not the largest number
of accelerator-covered operations. Preserve the whole model when it exports and
validates cleanly and does not recompute an expensive source result that is
designed to remain invariant across repeated calls. Split only to remove a
concrete obstacle, make implicit runtime state explicit, or expose such a proven
reusable tensor boundary.

## Preferred cut points

Consider boundaries in this order:

1. **Python control flow:** Keep loops, stopping rules, string handling,
   tokenization, and input-dependent branches outside an engine. Export the
   tensor computation invoked by one loop iteration.
2. **Modality towers:** Vision encoders, projectors, audio encoders, and language
   towers often already have meaningful tensor interfaces and different static
   sizing constraints.
3. **Reusable invariant computation:** If the public source computes a large
   representation once and intentionally reuses it while later tensor inputs
   vary, consider that representation as a cut. For example, an image encoder's
   features may remain fixed across several point prompts. Require a static,
   independently capturable tensor contract; do not encode cache ownership or
   invocation ordering in the engine bundle.
4. **State transitions:** Separate prefill from single-step decode when a source
   model hides KV state or grows a cache dynamically. Make old and new state
   explicit tensor bindings.
5. **Input-dependent graph construction:** Move mask construction, position
   lookup/interpolation, tiling, ragged packing, and similar shape-varying work
   outside when it can be supplied as a fixed-shape tensor input.
6. **Unsupported but equivalent operations:** Rewrite a localized operation in
   supported tensor math before splitting around individual transformer layers.

Avoid layer-by-layer cuts unless a demonstrated TensorRT or memory constraint
leaves no coarser verified boundary. Fine-grained cuts multiply binding metadata
and make correctness failures harder to localize without helping this skill's
engine-level objective.

## Boundary quality test

A candidate engine boundary is acceptable only when all answers are yes:

- Does it correspond to a source computation that can be invoked independently?
- Are all inputs, outputs, and carried state explicit tensors?
- Does every tensor dimension have one positive static value for this engine?
- Can the real reference execution provide a captured fixture at the boundary?
- Can the adapter be compared directly with that source computation?
- Can the engine run without a new user-provided TensorRT plugin?

If an answer is no, move the boundary or externalize the offending work. Do not
invent host orchestration metadata to compensate; orchestration is outside this
skill.

For an annotated application of these rules, read `lfm-vl-case-study.md`.

## Adapter rules

- Preserve parameter values and observable tensor behavior; restructuring code
  is allowed, changing the model's intended computation is not.
- Prefer explicit inputs over mutable module attributes or hidden caches.
- Replace data-dependent shapes with a fixed slot, pad, or window chosen from the
  representative source path.
- When a declared static contract still exports symbolic dimensions, replace
  symbolic repeat counts, inferred reshape dimensions, and generic output slices
  with their contract constants. Preserve the source operation order and rerun
  source-to-adapter parity after every such rewrite.
- Preserve FP32 reductions or normalization islands when an all-FP16 rewrite
  causes divergence.
- Record every externalized operation and rewrite in the decision report.
- Prove source-to-adapter parity before ONNX export. Later engine parity cannot
  establish that a wrong adapter represents the source model.
