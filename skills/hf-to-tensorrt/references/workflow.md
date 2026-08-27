# Engine export workflow

Use a separate workspace and `uv` project for one pinned source model. Every
gate produces evidence consumed by the next gate. Do not erase successful
earlier artifacts while iterating on a later failure.

## Gate 0: target and source preflight

- Confirm the machine matches the supported Zetic Thor profile using the process
  in `thor-target.md`.
- Record the GPU, compute capability, JetPack, TensorRT, CUDA, driver, OS, and
  native `trtexec` hash.
- Resolve the Hugging Face source to an immutable commit revision.
- Create a model-specific `pyproject.toml` and `uv.lock`.
- Run the user's reference inference twice with the same real input in `eval`
  mode. Require stable output before continuing.

Output: environment evidence, source revision, locked environment, and one
reproducible reference command.

## Gate 1: source probe

Instrument the successful reference path without changing its semantics.
Capture:

- invoked `nn.Module` hierarchy and call order;
- tensor inputs and outputs at candidate boundaries;
- shapes, dtypes, devices, and state mutation;
- Python loops, branching, tokenization, image transforms, and other non-tensor
  work surrounding the model;
- operations likely to depend on input content or create dynamic shapes.

If a pinned public processor produces the boundary tensors, capture its exact
tensor outputs for the real fixture. A hand-written equivalent must be checked
against those outputs, including operation order, coordinate conventions,
candidate selection, resize semantics, threshold comparison, dtypes, and shapes.
Preserve a failed comparison before changing the equivalent implementation.
This is a host-contract check only, not permission to run or claim a multi-engine
end-to-end validation.

Use one real representative invocation as the required v1 fixture. Random
tensors may supplement structural debugging but cannot replace the real fixture.

Output: source probe report and captured reference tensors.

## Gate 2: boundary design

Attempt the largest sensible tensor-to-tensor boundary first, then check whether
it would repeat an expensive source representation that is explicitly reusable
while later inputs vary. If either that condition holds or the whole boundary is
not a static export candidate, apply the decision rules in `decomposition.md`.
Do not accept a monolithic candidate when its reusable invariant boundary passes
the boundary quality test.

For every proposed engine record:

- the corresponding source module or computation;
- why the boundary is necessary;
- explicit ordered tensor inputs and outputs;
- static shapes and dtypes;
- work deliberately left outside the engine.

Do not define relationships among the resulting engines in the handoff
metadata. The decision report may mention source context, but each engine must
remain independently testable.

Output: a reviewed boundary decision and per-engine tensor contracts.

## Gate 3: adapter parity

Write the smallest adapter or equivalent module that realizes each contract.
Typical changes include exposing implicit cache as tensors, replacing unsupported
operations with equivalent tensor math, fixing shapes, moving input-dependent
graph construction outside the module, and preserving deliberate FP32 numeric
islands.

Run source and adapter from the same captured tensors. Apply the policy in
`parity-policy.md`. Fix the first divergent boundary before proceeding.

Output: generated adapter source, tests, and passing reference-to-adapter report.

## Gate 4: static ONNX

Export each adapter separately to ONNX with named inputs and outputs. Use external
data when required by the protobuf size limit. Validate that:

- the model passes the ONNX checker;
- every runtime input and output has a concrete static shape;
- all referenced external-data files exist;
- no unsupported custom domain is silently introduced;
- ONNX Runtime output passes the same engine-level parity policy against the
  adapter for the captured fixture.

Output: checked ONNX artifacts and passing adapter-to-ONNX report.

Use `scripts/inspect_onnx.py` for the structural and static-shape checks and
`scripts/run_onnx.py` for the captured-fixture execution.

## Gate 5: TensorRT build

Build on the target Thor using its native `trtexec`.

- Use FP16 for a plain FP32 ONNX graph.
- Use `--stronglyTyped` when the ONNX graph deliberately carries FP16 or mixed
  FP16/FP32 types.
- Do not generate or bundle a user custom plugin.
- Preserve the complete build command and log.

A successful parser or build exit code is insufficient. Require a non-empty
engine file.

Output: `.engine`, build log, build command, and hashes.

Use `scripts/build_engine.py`; do not hand-edit its selected precision mode after
a failed build.

## Gate 6: engine parity

Deserialize each engine on the target profile, verify all binding names, dtypes,
and shapes against the declared contract, and execute the captured fixture.
Compare every output using `scripts/compare_outputs.py`. Performance measurements
may be recorded but cannot make a correctness failure pass.

Output: a passing per-engine parity report containing raw measurements and the
policy version.

Use `scripts/inspect_engine.py` and `scripts/run_engine.py` for this gate.

## Gate 7: handoff

Place each engine's evidence in one directory using the filenames described in
`engine-bundle.md`. Generate aggregate `engine-bundle.json` with
`scripts/assemble_bundle.py`; do not transcribe bindings, hashes, build settings,
or parity measurements by hand. Include independent engine entries only; do not
include engine ordering or application orchestration. Run
`scripts/validate_bundle.py` and treat any error as a failed gate.

If the requested packaging unit is one engine, run the assembler separately for
each engine directory and validate every one-entry manifest. The aggregate is a
convenience index in that case, not the input a single-module packager should
silently split. Keep processor-contract sidecars in the reproducible workspace;
they are not fields in `zetic.engine_bundle.v1`.

Output: a validated `zetic.engine_bundle.v1` handoff plus the reproducible local
export workspace.
