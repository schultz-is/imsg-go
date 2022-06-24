// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

package imsg_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/schultz-is/imsg-go"
)

// This example constructs an IMsg and encodes it into an imsg in the current system's byte order.
func ExampleEncoder_Encode() {
	im, err := imsg.New(777, 888, 0, []byte("Howdy y'all!"))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	var b bytes.Buffer

	err = imsg.NewEncoder(&b).Encode(im)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(hex.Dump(b.Bytes()))
}

// This example constructs an IMsg by reading and decoding an imsg in little endian byte order.
func ExampleDecoder_Decode() {
	b, err := hex.DecodeString("090300001c000000780300005bb70000486f776479207927616c6c21")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	r := bytes.NewReader(b)

	var im imsg.IMsg

	err = imsg.NewDecoderWithByteOrder(r, binary.LittleEndian).Decode(&im)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Printf("%#v\n", im)
}
