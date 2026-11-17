// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

package imsg

import "encoding/binary"

// header holds the wire fields of an imsg, including the descriptor marker in length.
type header struct {
	typ    uint32
	length uint32
	peerID uint32
	pid    uint32
}

func newHeader(im IMsg) (header, error) {
	wireSize := len(im.Data) + HeaderSizeInBytes
	if wireSize > MaxSizeInBytes {
		return header{}, ErrLength
	}

	return header{
		typ:    im.Type,
		length: uint32(wireSize),
		peerID: im.PeerID,
		pid:    im.PID,
	}, nil
}

func (h header) marshal(byteOrder binary.ByteOrder, b []byte) {
	byteOrder.PutUint32(b[0:], h.typ)
	byteOrder.PutUint32(b[4:], h.length)
	byteOrder.PutUint32(b[8:], h.peerID)
	byteOrder.PutUint32(b[12:], h.pid)
}

// parseHeader validates the wire size but leaves descriptor handling to the caller.
func parseHeader(byteOrder binary.ByteOrder, b []byte) (header, error) {
	if len(b) < HeaderSizeInBytes {
		return header{}, ErrLength
	}

	rawLength := byteOrder.Uint32(b[4:])
	wireSize := rawLength &^ imsgFDMark
	if wireSize < HeaderSizeInBytes || wireSize > MaxSizeInBytes {
		return header{}, ErrLength
	}

	return header{
		typ:    byteOrder.Uint32(b[0:]),
		length: rawLength,
		peerID: byteOrder.Uint32(b[8:]),
		pid:    byteOrder.Uint32(b[12:]),
	}, nil
}

func (h header) wireSize() int {
	return int(h.length &^ imsgFDMark)
}

func (h header) hasFD() bool {
	return h.length&imsgFDMark != 0
}
