// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

package imsg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestHeader(t *testing.T) {
	tests := []struct {
		name     string
		in       header
		wantLE   string
		wantBE   string
		wantSize int
		wantFD   bool
	}{
		{
			name:     "empty imsg",
			in:       header{length: HeaderSizeInBytes},
			wantLE:   "00000000100000000000000000000000",
			wantBE:   "00000000000000100000000000000000",
			wantSize: HeaderSizeInBytes,
		},
		{
			name:     "ancillary data",
			in:       header{typ: 1234, length: 28, peerID: 888, pid: 61804},
			wantLE:   "d20400001c000000780300006cf10000",
			wantBE:   "000004d20000001c000003780000f16c",
			wantSize: 28,
		},
		{
			name:     "empty imsg with descriptor",
			in:       header{length: HeaderSizeInBytes | imsgFDMark},
			wantLE:   "00000000100000800000000000000000",
			wantBE:   "00000000800000100000000000000000",
			wantSize: HeaderSizeInBytes,
			wantFD:   true,
		},
		{
			name:     "max-valued fields",
			in:       header{typ: ^uint32(0), length: MaxSizeInBytes, peerID: ^uint32(0), pid: ^uint32(0)},
			wantLE:   "ffffffff00400000ffffffffffffffff",
			wantBE:   "ffffffff00004000ffffffffffffffff",
			wantSize: MaxSizeInBytes,
		},
	}

	for _, test := range tests {
		for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian, binary.NativeEndian} {
			t.Run(test.name+"/"+bo.String(), func(t *testing.T) {
				var want []byte
				if bo.Uint16([]byte{1, 0}) == 1 {
					want = mustDecodeHexString(t, test.wantLE)
				} else {
					want = mustDecodeHexString(t, test.wantBE)
				}

				b := make([]byte, HeaderSizeInBytes)
				test.in.marshal(bo, b)
				if !bytes.Equal(b, want) {
					t.Fatalf("header.marshal() got %x, want %x", b, want)
				}

				got, err := parseHeader(bo, want)
				if err != nil {
					t.Fatalf("parseHeader() unexpected error: %v", err)
				}
				if got != test.in {
					t.Fatalf("parseHeader() got %#v, want %#v", got, test.in)
				}
				if got.wireSize() != test.wantSize {
					t.Fatalf("header.wireSize() got %d, want %d", got.wireSize(), test.wantSize)
				}
				if got.hasFD() != test.wantFD {
					t.Fatalf("header.hasFD() got %v, want %v", got.hasFD(), test.wantFD)
				}
			})
		}
	}
}

func TestNewHeader(t *testing.T) {
	for _, dataSize := range []int{0, 1, MaxDataSizeInBytes, MaxDataSizeInBytes + 1} {
		t.Run(fmt.Sprintf("%d bytes", dataSize), func(t *testing.T) {
			im := IMsg{Type: 1234, PeerID: 888, PID: 61804, Data: make([]byte, dataSize)}
			got, err := newHeader(im)
			if dataSize > MaxDataSizeInBytes {
				if err != ErrLength {
					t.Fatalf("newHeader() got error %v, want %v", err, ErrLength)
				}
				return
			}
			if err != nil {
				t.Fatalf("newHeader() unexpected error: %v", err)
			}

			want := header{
				typ:    im.Type,
				length: uint32(dataSize + HeaderSizeInBytes),
				peerID: im.PeerID,
				pid:    im.PID,
			}
			if got != want {
				t.Fatalf("newHeader() got %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseHeaderShortInput(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		b := make([]byte, HeaderSizeInBytes)
		bo.PutUint32(b[4:], HeaderSizeInBytes)
		for n := range HeaderSizeInBytes {
			t.Run(fmt.Sprintf("%s/%d bytes", bo, n), func(t *testing.T) {
				if _, err := parseHeader(bo, b[:n]); err != ErrLength {
					t.Fatalf("parseHeader() got error %v, want %v", err, ErrLength)
				}
			})
		}
	}
}
