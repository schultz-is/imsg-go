// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build 386 || amd64 || arm || arm64 || loong64 || mipsle || mips64le || ppc64le || rsicv64 || wasm
// +build 386 amd64 arm arm64 loong64 mipsle mips64le ppc64le rsicv64 wasm

package imsg

import "encoding/binary"

var systemByteOrder = binary.LittleEndian
