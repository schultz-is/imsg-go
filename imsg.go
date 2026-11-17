// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

// Package imsg provides tools for working with OpenBSD's imsg IPC library.
package imsg

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"
)

const (
	// HeaderSizeInBytes is the size of an imsg header in bytes.
	HeaderSizeInBytes = 16
	// MaxSizeInBytes is the maximum allowed size of an imsg in bytes, including the header.
	MaxSizeInBytes = 16384
	// MaxDataSizeInBytes is the maximum allowed size of an imsg's ancillary data in bytes.
	MaxDataSizeInBytes = MaxSizeInBytes - HeaderSizeInBytes

	// OpenBSD reserves the high bit of the wire length for the file descriptor marker.
	imsgFDMark uint32 = 0x80000000
)

var (
	// ErrLength is returned any time the length of an imsg is outside of allowed or expected
	// parameters. The exception is when this error would mask an underlying error (e.g. an EOF.)
	ErrLength = errors.New("imsg: invalid message length")
	// ErrUnsupportedFD is returned when a message header indicates an accompanying file descriptor.
	// These data-only APIs do not support file descriptor passing.
	ErrUnsupportedFD = errors.New("imsg: file descriptor passing is not supported")
)

// An IMsg represents the contents of an imsg which are relevant to an application.
type IMsg struct {
	Type   uint32 // typically used to express the meaning of the message
	PeerID uint32 // free for any use; normally used to identify the message sender
	PID    uint32 // free for any use; normally used to identify the sending process
	Data   []byte // ancillary data to be transmitted with the imsg
}

// New will create an IMsg given the provided type, peer ID, PID, and ancillary data. When a PID
// of 0 is provided, the PID will be replaced with the result of os.Getpid(). If the ancillary data
// is too large, ErrLength will be returned.
func New(typ, peerID, pid uint32, data []byte) (*IMsg, error) {
	if len(data) > MaxDataSizeInBytes {
		return nil, ErrLength
	}

	im := &IMsg{
		Type:   typ,
		PeerID: peerID,
		PID:    pid,
	}
	if pid == 0 {
		im.PID = uint32(os.Getpid())
	}

	if len(data) > 0 {
		im.Data = make([]byte, len(data))
		_ = copy(im.Data, data)
	}

	return im, nil
}

// MarshalBinary implements the encoding.BinaryMarshaler interface. It returns the byte
// representation of an IMsg in the current system's byte order.
func (im IMsg) MarshalBinary() ([]byte, error) {
	return im.MarshalBinaryWithByteOrder(binary.NativeEndian)
}

// MarshalBinaryWithByteOrder returns the byte representation of an IMsg in the provided byte
// order. This is useful when generating imsgs for replay on remote systems which may have a
// different byte order than the current system. Since imsgs are intended for inter-process
// communication, most communication will occur on a single system and MarshalBinary will suffice.
func (im IMsg) MarshalBinaryWithByteOrder(byteOrder binary.ByteOrder) ([]byte, error) {
	h, err := newHeader(im)
	if err != nil {
		return nil, err
	}

	b := make([]byte, h.wireSize())
	h.marshal(byteOrder, b)
	_ = copy(b[16:], im.Data)

	return b, nil
}

// UnmarshalBinary implements the encoding.BinaryUnmarshaler interface. It returns an IMsg derived
// from a byte-formatted imsg in the current system's byte order.
// If an error is returned, im is left unchanged.
func (im *IMsg) UnmarshalBinary(b []byte) error {
	return im.UnmarshalBinaryWithByteOrder(binary.NativeEndian, b)
}

// UnmarshalBinaryWithByteOrder returns an IMsg derived from a byte-formatted imsg in the provided
// byte order. This is useful when replaying or inspecting imsgs recorded on a remote system which
// may have a different byte order than the current system. Since imsgs are intended for
// inter-process communication, most communication will occur on a single system and
// UnmarshalBinary will suffice.
// ErrLength is returned if the declared size is less than HeaderSizeInBytes, greater than
// MaxSizeInBytes, or exceeds len(b).
// ErrUnsupportedFD is returned if the header indicates an accompanying file descriptor.
// If an error is returned, im is left unchanged.
func (im *IMsg) UnmarshalBinaryWithByteOrder(byteOrder binary.ByteOrder, b []byte) error {
	h, err := parseHeader(byteOrder, b)
	if err != nil {
		return err
	}
	if h.hasFD() {
		return ErrUnsupportedFD
	}
	length := h.wireSize()
	if length > len(b) {
		return ErrLength
	}
	var data []byte
	if length > HeaderSizeInBytes {
		data = make([]byte, length-HeaderSizeInBytes)
		_ = copy(data, b[16:])
	}
	*im = IMsg{
		Type:   h.typ,
		PeerID: h.peerID,
		PID:    h.pid,
		Data:   data,
	}

	return nil
}

// An Encoder writes imsgs to an output stream.
type Encoder struct {
	w  io.Writer
	bo binary.ByteOrder
}

// NewEncoder returns a new encoder that writes to the provided io.Writer in the current system's
// byte order.
func NewEncoder(w io.Writer) *Encoder {
	return NewEncoderWithByteOrder(w, binary.NativeEndian)
}

// NewEncoderWithByteOrder returns a new encoder that writes to the provided io.Writer in the
// specified byte order.
func NewEncoderWithByteOrder(w io.Writer, byteOrder binary.ByteOrder) *Encoder {
	return &Encoder{
		w:  w,
		bo: byteOrder,
	}
}

// Encode writes the byte representation of the provided IMsg to the stream. ErrLength is returned
// if the resulting imsg would exceed the maximum allowed size. Providing a nil IMsg for encoding
// results in an effective NOOP, with nothing being written to the output stream and a nil error
// returned.
func (enc *Encoder) Encode(im *IMsg) error {
	if im == nil {
		return nil
	}

	h, err := newHeader(*im)
	if err != nil {
		return err
	}

	hdr := headerBufferPool.Get().(*headerBuffer)
	defer headerBufferPool.Put(hdr)

	h.marshal(enc.bo, hdr[:])
	_, err = enc.w.Write(hdr[:])
	if err != nil {
		return err
	}
	_, err = enc.w.Write(im.Data)
	return err
}

// A Decoder reads and decodes IMsgs from an input stream.
type Decoder struct {
	r  io.Reader
	bo binary.ByteOrder
}

// NewDecoder returns a new decoder that reads from the provided io.Reader in the current system's
// byte order.
func NewDecoder(r io.Reader) *Decoder {
	return NewDecoderWithByteOrder(r, binary.NativeEndian)
}

// NewDecoderWithByteOrder returns a new decoder that reads from the provided io.Reader in the
// specified byte order.
func NewDecoderWithByteOrder(r io.Reader, byteOrder binary.ByteOrder) *Decoder {
	return &Decoder{
		r:  r,
		bo: byteOrder,
	}
}

// Decode reads the next IMsg from the input stream and stores it in the value pointed to by im.
// ErrLength is returned if the total imsg size is less than HeaderSizeInBytes or greater than
// MaxSizeInBytes.
// ErrUnsupportedFD is returned if the header indicates an accompanying file descriptor.
// An EOF before any bytes of a new message are read is reported as io.EOF.
// An EOF after a message has started is reported as io.ErrUnexpectedEOF.
// If an error is returned, im is left unchanged.
func (dec *Decoder) Decode(im *IMsg) error {
	hdr := headerBufferPool.Get().(*headerBuffer)
	defer headerBufferPool.Put(hdr)

	_, err := io.ReadFull(dec.r, hdr[:])
	if err != nil {
		return err
	}

	h, err := parseHeader(dec.bo, hdr[:])
	if err != nil {
		return err
	}
	if h.hasFD() {
		return ErrUnsupportedFD
	}

	wireSize := h.wireSize()
	var data []byte
	if wireSize-HeaderSizeInBytes > 0 {
		data = make([]byte, wireSize-HeaderSizeInBytes)
		_, err = io.ReadFull(dec.r, data)
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
	}
	*im = IMsg{
		Type:   h.typ,
		PeerID: h.peerID,
		PID:    h.pid,
		Data:   data,
	}

	return nil
}

// headerBuffer is a byte array which can be used to temporarily store incoming or outgoing imsg
// headers.
type headerBuffer [HeaderSizeInBytes]byte

// headerBufferPool is a pool of fixed-size byte arrays which can be used during encoding and
// decoding in order to alleviate garbage collector strain.
var headerBufferPool = sync.Pool{
	New: func() any {
		return new(headerBuffer)
	},
}
