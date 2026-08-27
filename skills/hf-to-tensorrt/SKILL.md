---
name: hf-to-tensorrt
description: "Turn a runnable Hugging Face or PyTorch inference path into independently verified static TensorRT engines on Zetic's supported NVIDIA Thor environment. Use when an agent must inspect a model, choose export boundaries, author TensorRT-friendly adapters, export through ONNX, validate per-engine parity, and emit ZTC-ready engine metadata. Do not use for training, end-to-end multi-engine orchestration, non-Thor targets, dynamic-shape engines, quantization, or ZTC packaging."
---

# Hugging Face to TensorRT on Thor

Produce one or more independently verified TensorRT engine artifacts from a
working PyTorch inference path. The user should need to identify only the model
or checkpoint and the command that already performs representative inference.

The result is not complete until every declared engine runs on the supported
Thor environment, passes the Zetic engine-level parity policy, and is recorded
in a valid `zetic.engine_bundle.v1` manifest.

## Scope

- Work only on Zetic's supported Thor target profile.
- Use a separate `uv` project and `uv.lock` for each model export. Never modify
  the global Python environment.
- Use the canonical path `PyTorch reference -> adapter -> static ONNX ->
  TensorRT FP16 or strongly typed FP16/FP32 engine`.
- Treat each engine as an independent artifact. Do not validate or describe
  ordering, loops, cache ownership across engines, preprocessing, postprocessing,
  or end-to-end application behavior.
- Do not create custom TensorRT plugins. An engine must deserialize using only
  TensorRT itself and plugins already allowlisted in the target profile.
- Do not add dynamic shapes, optimization profiles, FP8, INT8, INT4, calibration,
  performance tuning, upload logic, or ZTC packaging.

## Required starting evidence

Before designing an export, establish all of the following:

1. A pinned Hugging Face repository revision or equivalent immutable source.
2. A user-provided Python inference entrypoint that succeeds in `eval` mode.
3. One real, representative invocation whose output is deterministic when rerun.
4. Access to the supported Thor build environment and its native `trtexec`.

If the reference inference does not run deterministically, stop and repair that
baseline first. Do not debug export and source behavior simultaneously.

Read [references/thor-target.md](references/thor-target.md) for target capture
and exact-profile matching. A captured fingerprint without a match against the
published supported profile is evidence, not a completed preflight.

## Workflow

Read [references/workflow.md](references/workflow.md) and follow its gates in
order. Persist the evidence from each gate so a later run can resume at the
first failed boundary rather than starting over.

Start by attempting the largest sensible model boundary. Before accepting it,
check whether the public inference path deliberately exposes an expensive
reusable tensor result that remains invariant while later inputs change, such as
image embeddings reused across point prompts. When that candidate passes the
boundary quality test in `decomposition.md`, split there even if the monolithic
graph also converts. When such reuse exists—or when the whole model contains
Python control flow, modality-specific towers, implicit state, or input-dependent
graph construction—read
[references/decomposition.md](references/decomposition.md) before choosing cuts.
The agent owns the boundary decision; do not require the user to name exportable
submodules.

For a concrete example of those decisions on a multimodal generation model,
read [references/lfm-vl-case-study.md](references/lfm-vl-case-study.md) and inspect
its standalone asset. Use its reasoning as an analogy, not its four-module layout
as a template.

It is acceptable—and often necessary—to write a TensorRT-friendly adapter or a
behaviorally equivalent reimplementation. Such code must be derived only from
the public model source and the user's program, must expose all engine state as
explicit tensor I/O, and must pass both reference-to-adapter and
adapter-to-engine parity. Never claim success for an unverified approximation.

## Output contract

Keep generated adapters, export scripts, tests, ONNX files, engines, `uv.lock`,
and decision notes in a reproducible export workspace. The handoff manifest is
`engine-bundle.json` beside the files it references.

Read [references/engine-bundle.md](references/engine-bundle.md) before assembling
the handoff. Generate it from build, engine inspection, target, and parity
evidence using `scripts/assemble_bundle.py`; never manually copy extracted
fields into the manifest. Validate the result with:

```sh
uv run --with jsonschema \
  python <skill-dir>/scripts/validate_bundle.py <export-dir>/engine-bundle.json
```

Metadata must be extracted from the source revision, target environment,
TensorRT engine bindings, build command, file hashes, and parity report. Do not
ask the user to hand-author tensor or build metadata.

For the engine-level numerical gate, read
[references/parity-policy.md](references/parity-policy.md) and use its comparator.
The policy owns its tolerances; an agent must not relax them after observing a
failure.

For static ONNX inspection, the native `trtexec` build, engine binding extraction,
and a generic static-engine runner, read
[references/onnx-tensorrt.md](references/onnx-tensorrt.md). Use those scripts
instead of recreating their mechanics in model-specific adapter code.

## Completion and failure

Report success only when all workflow gates pass and the bundle validator exits
zero. Report each engine path, its binding contract, precision mode, and parity
policy result.

When conversion cannot finish without a prohibited feature or when parity fails,
leave the reproducible workspace intact and report the first unresolved gate,
the minimized failure evidence, attempted rewrites, and the next technical
capability required. A generated `.engine` that has not passed inference and
parity is a failed result, not a partial success.
