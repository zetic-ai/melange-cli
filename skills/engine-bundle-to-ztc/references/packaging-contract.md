# Single-engine ZTC packaging contract

The input is one validated `zetic.engine_bundle.v1` manifest containing exactly
one independent engine. The packager derives ZTC metadata; a user does not
transcribe tensor bindings, source identity, compatibility, or hashes.

## Derived metadata

- Metadata version: `0.3.0`.
- Model identity: Hugging Face repository, immutable revision, architecture, and
  repository basename from the engine bundle.
- Module identity: engine entry name, chunk ID `0`, and engine filename.
- Target: `TENSORRT_FP16` with NVIDIA GPU compatibility.
- Quantization label: `FP16` for both `fp16` and `strongly_typed` v1 engines.
- I/O: ordered binding names, original names, dtypes, ranks, and static shapes
  copied from the validated engine entry.
- Target-model and package IDs: deterministic short SHA-256 identities derived
  from module name, target, filename, compatibility, and package membership.

The encrypted container stores the engine as its only chunk. Host processor
sidecars remain outside the `.ztc`; they are reproducibility evidence, not engine
chunks or orchestration metadata.

## Public and private artifacts

The metadata and validation report contain no key. The private handoff contains
the randomly generated 32-byte encryption key and is created owner-readable only.
Anyone handling the private file must treat it as a secret: its possession allows
the encrypted package to be opened.

## Validation meaning

The packer reopens the completed container using the generated key, checks the
stored metadata against the derived metadata, confirms a different key fails,
extracts chunk `0`, and compares the extracted bytes with the source engine hash.
This is a package round-trip, not an engine execution or model-level evaluation.
