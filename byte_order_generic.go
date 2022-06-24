// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build !386 && !amd64 && !arm && !arm64 && !loong64 && !mips && !mipsle && !mips64 && !mips64le && !ppc64 && !ppc64le && !riscv64 && !s390x && !wasm
// +build !386,!amd64,!arm,!arm64,!loong64,!mips,!mipsle,!mips64,!mips64le,!ppc64,!ppc64le,!riscv64,!s390x,!wasm

package imsg

import (
	"encoding/binary"
	"unsafe"
)

var systemByteOrder = binary.LittleEndian

func init() {
	// This is a fallback way of determining system byte order on systems which aren't accounted for
	// in the above instruction set build tags.
	var x uint16 = 1
	if *(*byte)(unsafe.Pointer(&x)) == 0 {
		systemByteOrder = binary.BigEndian
	}
}
