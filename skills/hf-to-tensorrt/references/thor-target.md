# Supported Thor target

TensorRT engines are valid only for Zetic's supported Thor board profile. A
machine being manufactured by NVIDIA is not sufficient. The profile requires an
exact match on the observed GPU identity, compute capability, JetPack, TensorRT,
CUDA, driver, OS release, and native `trtexec` binary hash.

Capture a candidate machine from the model's locked `uv` environment:

```sh
uv run python <skill-dir>/scripts/check_thor_env.py \
  --trtexec /usr/src/tensorrt/bin/trtexec \
  --output thor-target.json
```

The command reads CUDA identity from `nvidia-smi` before falling back to torch,
so it can run before the model environment installs PyTorch. It fails unless
CUDA is available, the device identifies as Thor, and all required fingerprint
fields can be read. It does not by itself prove that the machine matches Zetic's
board: compare the captured document with the version-controlled supported
profile using `--expected`:

```sh
uv run python <skill-dir>/scripts/check_thor_env.py \
  --trtexec /usr/src/tensorrt/bin/trtexec \
  --expected <skill-dir>/references/supported-thor-profile.json \
  --output thor-target.json
```

Every fingerprint field must match exactly. Never edit a captured value to make
the comparison pass. A Zetic profile revision is a deliberate compatibility
release; it must be validated by rebuilding and running representative engines
on that board before updating the expected document.

The normative `zetic-thor-v1` snapshot is
`supported-thor-profile.json` in this directory. It was captured on Zetic's
NVIDIA Jetson AGX Thor Developer Kit on 2026-08-25. Publishing a different
fingerprint under the same profile name is prohibited; release a new profile
identifier after compatibility validation instead.
