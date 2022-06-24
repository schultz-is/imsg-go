// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build mips || mips64 || ppc64 || s390x
// +build mips mips64 ppc64 s390x

package imsg

import "encoding/binary"

var systemByteOrder = binary.BigEndian
