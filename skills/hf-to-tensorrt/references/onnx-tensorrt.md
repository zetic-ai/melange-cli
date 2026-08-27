# Static ONNX and TensorRT tools

Use the skill's deterministic scripts instead of reconstructing ONNX checks,
`trtexec` flags, engine inspection, or CUDA buffer handling in each model
adapter.

## Inspect the ONNX

```sh
uv run --with onnx --with numpy \
  python <skill-dir>/scripts/inspect_onnx.py model.onnx \
  --output onnx-metadata.json
```

The inspector runs the ONNX checker, resolves and hashes external-data files,
requires static runtime bindings, records opset domains, and recommends either
`fp16` or `strongly_typed`. Q/DQ nodes, BF16/FP64/complex types, and low-bit
tensor types are reported but are unsupported by this v1 FP16/FP32 skill.

## Run ONNX parity

Run the captured fixture with a provider installed in the model's locked
environment:

```sh
uv run --with onnxruntime --with numpy \
  python <skill-dir>/scripts/run_onnx.py model.onnx \
  --inputs captured-inputs.npz \
  --outputs onnx-outputs.npz
```

The runner requires the NPZ input names, dtypes, and shapes to match the static
ONNX bindings exactly. A different provider may be selected with `--provider`
only when that provider is present in the locked environment. Record and report
the first missing-kernel error instead of silently skipping ONNX execution.

`--disable-optimizations` may diagnose an execution-provider fusion with missing
type coverage, but its output still must pass parity. If an all-FP16 graph runs
and fails parity, do not relax the policy. A strongly typed mixed graph may
preserve source FP32 computation and expose deliberate FP16 bindings, but it is
a new adapter candidate: rerun source-to-adapter and adapter-to-ONNX parity from
the beginning.

## Build the engine

```sh
uv run --with onnx --with numpy \
  python <skill-dir>/scripts/build_engine.py model.onnx \
  --engine model.engine \
  --trtexec /usr/src/tensorrt/bin/trtexec \
  --log trtexec.log \
  --result build.json
```

The builder accepts only fully static ONNX, selects weak FP16 for a plain FP32
graph and `--stronglyTyped` when the graph already carries FP16/mixed precision,
and refuses Q/DQ or low-bit graphs. It records the exact command and hashes. A
successful build is not a parity result.

## Inspect and execute the engine

Run these commands in the same locked environment used to build the engine, with
the Python TensorRT bindings matching the native `trtexec`:

```sh
uv run --with numpy \
  python <skill-dir>/scripts/inspect_engine.py model.engine \
  --output engine-metadata.json

uv run --with numpy \
  python <skill-dir>/scripts/run_engine.py model.engine \
  --inputs captured-inputs.npz \
  --outputs engine-outputs.npz
```

`ZETIC_TENSORRT_PYTHON_PATH` may point to the system TensorRT Python package.
The default is `/usr/lib/pythonX.Y/dist-packages`. Do not use a standalone wheel
whose TensorRT libraries differ from the `trtexec` that built the plan.

Then apply `parity-policy.md` to the adapter and engine output `.npz` files.
