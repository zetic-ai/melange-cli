---
name: engine-bundle-to-ztc
description: "Package one validated Zetic Thor TensorRT engine bundle into one encrypted single-module .ztc and prove its metadata and engine bytes round-trip through Zetic's native loader. Use after hf-to-tensorrt when a one-entry zetic.engine_bundle.v1 handoff is ready. Do not use to build engines, combine multiple engines, upload packages, or validate end-to-end inference."
---

# Engine bundle to ZTC

Turn exactly one independently validated Thor `.engine` into exactly one
encrypted `.ztc`. The user supplies the one-entry manifest and a package base
key; derive all package metadata from the manifest and the supported target.

## Required environment

- Use the same model-specific `uv` project that produced the engine bundle. It
  must contain `jsonschema` for the public bundle validator.
- Require Zetic's product-supplied `mlange_ztc` native binding for the current
  Python ABI. Do not download an arbitrary wheel or reimplement the encrypted
  container format. If the binding is outside the `uv` environment, set
  `ZETIC_MLANGE_ZTC_PYTHONPATH` to its Python package directory.
- Require the sibling `hf-to-tensorrt` skill. Its validator is the authority for
  engine, parity-report, and hash consistency before packaging.

Stop before writing a package if any precondition is missing.

## Packaging rule

Accept only a validated `zetic.engine_bundle.v1` manifest with exactly one engine
entry and target profile `zetic-thor-v1`. Never silently split a multi-entry
manifest and never place multiple engines in one `.ztc`.

Read [references/packaging-contract.md](references/packaging-contract.md) when
auditing the derived metadata or integrating a package handoff.

Run:

```sh
ZETIC_MLANGE_ZTC_PYTHONPATH=<supported-native-python-dir> \
  uv run python <skill-dir>/scripts/package_ztc.py \
  <export-dir>/engine-bundle-<engine-name>.json \
  --output-dir <export-dir>/ztc/<engine-name> \
  --package-base-key <safe-package-name>
```

The script first runs the public engine-bundle validator, then generates a fresh
32-byte encryption key, derives metadata, calls the native packer, reopens the
container, rejects a wrong key, extracts chunk 0, and requires the restored
engine SHA-256 to equal the manifest.

## Output and secret handling

The output directory is new and contains:

- `<package-key>.ztc`: encrypted single-module package;
- `<package-key>.metadata.json`: non-secret derived metadata;
- `<package-key>.report.json`: non-secret validation evidence;
- `<package-key>.private.json`: encryption key handoff, created with mode `0600`.

Never print, paste, commit, upload as ordinary metadata, or include the private
manifest in a PR. Do not commit the `.ztc`, source engine, captured tensors, or
model weights. Upload and backend registration require separate user authority.

## Completion boundary

Report success only when the report status is `passed` and every declared check
passes: container open, wrong-key rejection, exactly one module, source identity,
Thor compatibility, binding metadata, and byte-identical engine round-trip.

This proves package structure and contents only. It does not prove TensorRT
execution from the container, multi-engine orchestration, application-level
inference, upload, registration, or device-farm behavior.
