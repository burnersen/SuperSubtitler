//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"sync"
	"syscall"
)

// nvidiaPresent reports whether an NVIDIA driver that can run CUDA is
// installed: every such driver puts nvcuda.dll into System32. Without it the
// graphics-card path can only fail, so the 1.3 GB of CUDA libraries are not
// downloaded and the engine runs on the processor straight away.
var nvidiaPresent = sync.OnceValue(func() bool {
	h, err := syscall.LoadLibrary("nvcuda.dll")
	if err != nil {
		return false
	}
	_ = syscall.FreeLibrary(h)
	return true
})

// cudaWanted: install the CUDA libraries (install_cuda, and a card to use them).
func cudaWanted() bool {
	return appSettings.installCUDA && nvidiaPresent()
}

// engineDevice is the device and number format the engine starts with:
// device=cuda without an NVIDIA card goes to the processor directly, with the
// processor's format, instead of failing first and falling back.
func engineDevice(lowMem bool) (device, compute string) {
	device, compute = appSettings.device, appSettings.computeType
	if lowMem {
		compute = appSettings.computeTypeLowMem
	}
	if device == "cuda" && !nvidiaPresent() {
		return "cpu", "int8"
	}
	return device, compute
}

// gpuSummary is one line for -version and the start of a run.
func gpuSummary() string {
	if nvidiaPresent() {
		return "graphics: NVIDIA (CUDA)"
	}
	return "graphics: no NVIDIA card — the processor does the work"
}
