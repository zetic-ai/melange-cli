# Engine bundle v1

`engine-bundle.json` is the public handoff between a completed local export and
a future ZTC packager. Its schema identifier is `zetic.engine_bundle.v1`; the
normative JSON Schema is `engine-bundle.schema.json` in this directory.

The manifest contains only independent TensorRT engines and the evidence needed
to identify and package them. It does not describe engine ordering, loops,
preprocessing, postprocessing, cache ownership across engines, or application
semantics. Reproducible processor-contract sidecars may accompany the export
workspace, but they are deliberately not bundle fields.

## Required evidence

- immutable Hugging Face source repository, revision, and architecture;
- exact Zetic Thor target profile and observed software/hardware fingerprint;
- one or more uniquely named engine files and SHA-256 digests;
- FP16 or strongly typed build mode;
- ordered input and output binding names, dtypes, and static shapes;
- source ONNX digest and the exact `trtexec` command;
- a passing per-engine parity report and policy version.

Paths are relative to the directory containing `engine-bundle.json`, use `/` as
the separator, and may not escape that directory. The referenced engine and
parity report files must be non-empty and match their declared SHA-256 values.

## Automatic assembly

Do not hand-author the manifest. For each independent engine, create a directory
whose basename is the engine name and place these generated files in it:

- the `.engine` named by `engine-metadata.json`;
- `build.json` from `scripts/build_engine.py`;
- `engine-metadata.json` from `scripts/inspect_engine.py`;
- `parity.json` from `scripts/compare_outputs.py`.

Then assemble the bundle from those evidence directories:

```sh
uv run --with jsonschema \
  python <skill-dir>/scripts/assemble_bundle.py \
  --output <export-dir>/engine-bundle.json \
  --repo-id owner/model \
  --revision 0123456789abcdef0123456789abcdef01234567 \
  --architecture ExampleModel \
  --target <export-dir>/thor-target.json \
  --engine-dir <export-dir>/artifacts/encoder
```

Repeat `--engine-dir` for additional independent engines. The assembler checks
that ONNX/build contracts, deserialized engine bindings, engine bytes, and
parity measurements agree, then atomically writes a manifest only if the bundle
validator passes.

When each engine will become a separate single-module package, also invoke the
assembler once per engine directory, giving each output a distinct name such as
`engine-bundle-vision_encoder.json`. Each such manifest must contain exactly one
engine entry and must pass `validate_bundle.py` independently. An optional
multi-entry `engine-bundle.json` may still be produced as an aggregate inventory,
but it does not replace the one-entry packaging inputs.

The manifest deliberately omits ZTC chunk IDs, target model IDs, package IDs,
secrets, and internal compatibility objects. Those belong to the private
packager and must be derived there.

## Example

```json
{
  "schema": "zetic.engine_bundle.v1",
  "source": {
    "repo_id": "owner/model",
    "revision": "0123456789abcdef0123456789abcdef01234567",
    "architecture": "ExampleForConditionalGeneration"
  },
  "target": {
    "profile": "zetic-thor-v1",
    "fingerprint": {
      "gpu_name": "NVIDIA Thor",
      "compute_capability": "10.0",
      "jetpack_version": "example",
      "tensorrt_version": "example",
      "cuda_version": "example",
      "driver_version": "example",
      "os_release": "example",
      "trtexec_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    }
  },
  "engines": [
    {
      "name": "vision_encoder",
      "file": "artifacts/vision_encoder/model.engine",
      "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "precision": "fp16",
      "bindings": {
        "inputs": [
          {"name": "pixel_values", "dtype": "float32", "shape": [1, 3, 224, 224]}
        ],
        "outputs": [
          {"name": "last_hidden_state", "dtype": "float16", "shape": [1, 256, 768]}
        ]
      },
      "build": {
        "onnx_sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
        "trtexec_command": [
          "/usr/src/tensorrt/bin/trtexec",
          "--onnx=model.onnx",
          "--saveEngine=model.engine",
          "--fp16"
        ]
      },
      "parity": {
        "policy": "zetic.engine_parity.v1",
        "status": "passed",
        "fixture_count": 1,
        "report_file": "artifacts/vision_encoder/parity.json",
        "report_sha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
      }
    }
  ]
}
```

The values shown as `example` are illustrative; a real bundle must contain the
values measured from the supported target. The validator checks structure,
paths, file hashes, unique engine and binding names, and that passing parity
evidence describes the declared engine outputs.
