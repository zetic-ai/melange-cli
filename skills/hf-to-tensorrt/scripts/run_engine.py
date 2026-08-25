#!/usr/bin/env python3
"""Run one static TensorRT engine from named NPZ inputs and save NPZ outputs."""

from __future__ import annotations

import argparse
import ctypes
from pathlib import Path
from typing import Any

import numpy as np

from inspect_engine import import_tensorrt


class EngineError(RuntimeError):
    pass


class CudaRuntime:
    HOST_TO_DEVICE = 1
    DEVICE_TO_HOST = 2

    def __init__(self) -> None:
        self.lib = ctypes.CDLL("libcudart.so")
        self.lib.cudaMalloc.argtypes = [ctypes.POINTER(ctypes.c_void_p), ctypes.c_size_t]
        self.lib.cudaFree.argtypes = [ctypes.c_void_p]
        self.lib.cudaMemcpy.argtypes = [
            ctypes.c_void_p,
            ctypes.c_void_p,
            ctypes.c_size_t,
            ctypes.c_int,
        ]
        self.lib.cudaStreamCreate.argtypes = [ctypes.POINTER(ctypes.c_void_p)]
        self.lib.cudaStreamDestroy.argtypes = [ctypes.c_void_p]
        self.lib.cudaStreamSynchronize.argtypes = [ctypes.c_void_p]

    @staticmethod
    def check(code: int, operation: str) -> None:
        if code != 0:
            raise EngineError(f"{operation} failed with CUDA error {code}")


class StaticEngine:
    def __init__(self, path: Path):
        self.path = path.resolve()
        self.cuda: CudaRuntime | None = None
        self.runtime: Any = None
        self.engine: Any = None
        self.context: Any = None
        self.stream = ctypes.c_void_p()
        self.buffers: dict[str, tuple[Any, tuple[int, ...], np.dtype[Any]]] = {}
        self.inputs: list[str] = []
        self.outputs: list[str] = []
        if not self.path.is_file() or self.path.stat().st_size == 0:
            raise EngineError(f"missing or empty engine: {self.path}")
        try:
            self.trt = import_tensorrt()
            self.cuda = CudaRuntime()
            self.logger = self.trt.Logger(self.trt.Logger.ERROR)
            self.runtime = self.trt.Runtime(self.logger)
            self.engine = self.runtime.deserialize_cuda_engine(self.path.read_bytes())
            if self.engine is None:
                raise EngineError(f"could not deserialize engine: {self.path}")
            self.context = self.engine.create_execution_context()
            if self.context is None:
                raise EngineError(f"could not create execution context: {self.path}")
            self.cuda.check(
                self.cuda.lib.cudaStreamCreate(ctypes.byref(self.stream)),
                "cudaStreamCreate",
            )
            for index in range(self.engine.num_io_tensors):
                name = self.engine.get_tensor_name(index)
                shape = tuple(int(value) for value in self.engine.get_tensor_shape(name))
                if any(value <= 0 for value in shape):
                    raise EngineError(f"dynamic or invalid shape for {name!r}: {shape}")
                dtype = np.dtype(self.trt.nptype(self.engine.get_tensor_dtype(name)))
                pointer = ctypes.c_void_p()
                nbytes = int(np.prod(shape)) * dtype.itemsize
                self.cuda.check(
                    self.cuda.lib.cudaMalloc(ctypes.byref(pointer), max(1, nbytes)),
                    f"cudaMalloc({name})",
                )
                if pointer.value is None:
                    raise EngineError(f"cudaMalloc returned null for {name!r}")
                self.buffers[name] = (pointer, shape, dtype)
                if not self.context.set_tensor_address(name, int(pointer.value)):
                    raise EngineError(f"could not bind TensorRT tensor {name!r}")
                target = (
                    self.inputs
                    if self.engine.get_tensor_mode(name) == self.trt.TensorIOMode.INPUT
                    else self.outputs
                )
                target.append(name)
        except Exception:
            self.close()
            raise

    def infer(self, feed: dict[str, np.ndarray[Any, Any]]) -> dict[str, np.ndarray[Any, Any]]:
        missing = sorted(set(self.inputs) - set(feed))
        unexpected = sorted(set(feed) - set(self.inputs))
        if missing or unexpected:
            raise EngineError(f"invalid inputs: missing={missing}, unexpected={unexpected}")
        for name in self.inputs:
            pointer, shape, dtype = self.buffers[name]
            array = np.ascontiguousarray(feed[name], dtype=dtype)
            if array.shape != shape:
                raise EngineError(f"{name!r} has shape {array.shape}; expected {shape}")
            self.cuda.check(
                self.cuda.lib.cudaMemcpy(
                    pointer,
                    array.ctypes.data_as(ctypes.c_void_p),
                    array.nbytes,
                    self.cuda.HOST_TO_DEVICE,
                ),
                f"copy {name} to device",
            )
        if self.stream.value is None:
            raise EngineError("CUDA stream is null")
        if not self.context.execute_async_v3(int(self.stream.value)):
            raise EngineError("TensorRT execution returned false")
        self.cuda.check(
            self.cuda.lib.cudaStreamSynchronize(self.stream),
            "cudaStreamSynchronize",
        )
        outputs = {}
        for name in self.outputs:
            pointer, shape, dtype = self.buffers[name]
            array = np.empty(shape, dtype=dtype)
            self.cuda.check(
                self.cuda.lib.cudaMemcpy(
                    array.ctypes.data_as(ctypes.c_void_p),
                    pointer,
                    array.nbytes,
                    self.cuda.DEVICE_TO_HOST,
                ),
                f"copy {name} to host",
            )
            outputs[name] = array
        return outputs

    def close(self) -> None:
        if self.cuda is not None:
            for pointer, _, _ in self.buffers.values():
                if pointer.value is not None:
                    self.cuda.lib.cudaFree(pointer)
                    pointer.value = None
        self.buffers.clear()
        if self.cuda is not None and self.stream.value is not None:
            self.cuda.lib.cudaStreamDestroy(self.stream)
            self.stream.value = None
        self.context = None
        self.engine = None
        self.runtime = None

    def __enter__(self) -> "StaticEngine":
        return self

    def __exit__(self, exc_type: Any, exc: Any, traceback: Any) -> None:
        self.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", type=Path)
    parser.add_argument("--inputs", type=Path, required=True)
    parser.add_argument("--outputs", type=Path, required=True)
    args = parser.parse_args()
    try:
        with np.load(args.inputs, allow_pickle=False) as archive:
            feed = {name: archive[name] for name in archive.files}
        with StaticEngine(args.engine) as engine:
            outputs = engine.infer(feed)
        args.outputs.parent.mkdir(parents=True, exist_ok=True)
        np.savez(args.outputs, **outputs)
    except (EngineError, ImportError, OSError, ValueError) as exc:
        parser.error(str(exc))
    print(f"wrote: {args.outputs}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
