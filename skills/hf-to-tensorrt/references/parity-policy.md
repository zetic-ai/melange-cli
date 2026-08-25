# Zetic engine parity policy v1

The `zetic.engine_parity.v1` policy compares one real captured fixture at one
independent engine boundary. It is deliberately stricter and smaller than a
model-level quality evaluation: it decides only whether the TensorRT engine
reproduces the corresponding adapter outputs.

Use `scripts/compare_outputs.py` to apply the policy. Give it two `.npz` files
whose keys are output binding names:

```sh
uv run --with numpy \
  python <skill-dir>/scripts/compare_outputs.py \
  --reference adapter-outputs.npz \
  --actual engine-outputs.npz \
  --report parity.json
```

The script owns all thresholds. Do not add flags or edit the report to relax a
failed comparison.

## Required invariants

- Reference and actual output name sets must match exactly.
- Every output shape must match exactly.
- NaN or infinity in either output fails the comparison.
- Boolean and integer tensors must have the same dtype and match elementwise.
- Floating tensors may differ in width. The actual engine dtype selects the
  tolerance:

| Actual dtype | Relative tolerance | Absolute tolerance |
| --- | ---: | ---: |
| `float16` | `1e-2` | `1e-2` |
| `float32` or `float64` | `1e-4` | `1e-4` |

Every floating element must satisfy `abs(actual-reference) <= atol +
rtol*abs(reference)`. There is no allowed mismatch fraction in v1. Unsupported
dtypes fail closed.

The report records raw maximum absolute and relative errors, mismatch counts,
file hashes, and the policy version. A bundle may reference only a report whose
status is `passed`.
