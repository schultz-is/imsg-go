// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

// Package imsg provides tools for working with OpenBSD's imsg IPC library.
package imsg

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	// HeaderSizeInBytes is the size of an imsg header in bytes.
	HeaderSizeInBytes = 16
	// MaxSizeInBytes is the maximum allowed size of an imsg in bytes, including the header.
	MaxSizeInBytes = 16384
	// MaxDataSizeInBytes is the maximum allowed size of an imsg's ancillary data in bytes.
	MaxDataSizeInBytes = MaxSizeInBytes - HeaderSizeInBytes
	// imsgFDMark is the bit set in the length field when an FD is attached.
	imsgFDMark = 0x80000000
)

var (
	// ErrLength is returned any time the length of an imsg is outside of allowed or expected
	// parameters. The exception is when this error would mask an underlying error (e.g. an EOF.)
	ErrLength = errors.New("imsg: data is too long")
)

// An IMsg represents the contents of an imsg which are relevant to an application.
type IMsg struct {
	Type   uint32 // typically used to express the meaning of the message
	PeerID uint32 // free for any use; normally used to identify the message sender
	PID    uint32 // free for any use; normally used to identify the sending process
	Data   []byte // ancillary data to be transmitted with the imsg
	FD     int    // file descriptor to pass; -1 indicates no FD
}

// New will create an IMsg given the provided type, peer ID, PID, and ancillary data. When a PID
// of 0 is provided, the PID will be replaced with the result of os.Getpid(). If the ancillary data
// is too large, ErrLength will be returned. The FD field is initialized to -1 (no file descriptor).
func New(typ, peerID, pid uint32, data []byte) (*IMsg, error) {
	return NewWithFD(typ, peerID, pid, data, -1)
}

// NewWithFD will create an IMsg with a file descriptor. When a PID of 0 is provided, the PID will
// be replaced with the result of os.Getpid(). If the ancillary data is too large, ErrLength will
// be returned. Set fd to -1 to indicate no file descriptor should be passed.
func NewWithFD(typ, peerID, pid uint32, data []byte, fd int) (*IMsg, error) {
	if len(data) > MaxDataSizeInBytes {
		return nil, ErrLength
	}

	im := &IMsg{
		Type:   typ,
		PeerID: peerID,
		PID:    pid,
		FD:     fd,
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
	wireSize := len(im.Data) + HeaderSizeInBytes
	if wireSize > MaxSizeInBytes {
		return nil, ErrLength
	}

	b := make([]byte, wireSize)
	byteOrder.PutUint32(b[0:], im.Type)
	byteOrder.PutUint16(b[4:], uint16(wireSize))
	byteOrder.PutUint16(b[6:], uint16(0))
	byteOrder.PutUint32(b[8:], im.PeerID)
	byteOrder.PutUint32(b[12:], im.PID)
	_ = copy(b[16:], im.Data)

	return b, nil
}

// UnmarshalBinary implements the encoding.BinaryUnmarshaler interface. It returns an IMsg derived
// from a byte-formatted imsg in the current system's byte order.
func (im *IMsg) UnmarshalBinary(b []byte) error {
	return im.UnmarshalBinaryWithByteOrder(binary.NativeEndian, b)
}

// UnmarshalBinaryWithByteOrder returns an IMsg derived from a byte-formatted imsg in the provided
// byte order. This is useful when replaying or inspecting imsgs recorded on a remote system which
// may have a different byte order than the current system. Since imsgs are intended for
// inter-process communication, most communication will occur on a single system and
// UnmarshalBinary will suffice.
func (im *IMsg) UnmarshalBinaryWithByteOrder(byteOrder binary.ByteOrder, b []byte) error {
	if len(b) < HeaderSizeInBytes {
		return ErrLength
	}

	im.Type = byteOrder.Uint32(b[0:])
	length := int(byteOrder.Uint16(b[4:]))
	if length > MaxSizeInBytes {
		return ErrLength
	}
	if length > len(b) {
		return ErrLength
	}
	im.PeerID = byteOrder.Uint32(b[8:])
	im.PID = byteOrder.Uint32(b[12:])
	if length > HeaderSizeInBytes {
		im.Data = make([]byte, length-HeaderSizeInBytes)
		_ = copy(im.Data, b[16:])
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

	wireSize := len(im.Data) + HeaderSizeInBytes
	if wireSize > MaxSizeInBytes {
		return ErrLength
	}

	hdr := headerBufferPool.Get().(*headerBuffer)
	enc.bo.PutUint32(hdr[0:], im.Type)
	enc.bo.PutUint16(hdr[4:], uint16(wireSize))
	enc.bo.PutUint16(hdr[6:], uint16(0))
	enc.bo.PutUint32(hdr[8:], im.PeerID)
	enc.bo.PutUint32(hdr[12:], im.PID)
	_, err := enc.w.Write(hdr[:])
	if err != nil {
		return err
	}
	headerBufferPool.Put(hdr)
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
// ErrLength is returned if the imsg's size is greater than the maximum allowed size for an imsg.
func (dec *Decoder) Decode(im *IMsg) error {
	hdr := headerBufferPool.Get().(*headerBuffer)
	_, err := dec.r.Read(hdr[:])
	if err != nil {
		return err
	}

	wireSize := dec.bo.Uint16(hdr[4:])
	if wireSize > MaxSizeInBytes {
		return ErrLength
	}

	im.Type = dec.bo.Uint32(hdr[0:])
	im.PeerID = dec.bo.Uint32(hdr[8:])
	im.PID = dec.bo.Uint32(hdr[12:])
	headerBufferPool.Put(hdr)

	if wireSize-HeaderSizeInBytes > 0 {
		data := make([]byte, wireSize-HeaderSizeInBytes)
		_, err = dec.r.Read(data)
		if err != nil {
			return err
		}
		im.Data = data
	}

	return nil
}

// headerBuffer is a byte array which can be used to temporarily store incoming or outgoing imsg
// headers.
type headerBuffer [HeaderSizeInBytes]byte

// headerBufferPool is a pool of fixed-size byte arrays which can be used during encoding and
// decoding in order to alleviate garbage collector strain.
var headerBufferPool = sync.Pool{
	New: func() interface{} {
		return new(headerBuffer)
	},
}

// A UnixEncoder writes imsgs with file descriptor passing support to a Unix domain socket.
type UnixEncoder struct {
	conn *net.UnixConn
	bo   binary.ByteOrder
}

// NewUnixEncoder returns a new Unix encoder that writes to the provided Unix domain socket
// in the current system's byte order. This encoder supports file descriptor passing via
// SCM_RIGHTS control messages.
func NewUnixEncoder(conn *net.UnixConn) *UnixEncoder {
	return NewUnixEncoderWithByteOrder(conn, binary.NativeEndian)
}

// NewUnixEncoderWithByteOrder returns a new Unix encoder that writes to the provided Unix
// domain socket in the specified byte order.
func NewUnixEncoderWithByteOrder(conn *net.UnixConn, byteOrder binary.ByteOrder) *UnixEncoder {
	return &UnixEncoder{
		conn: conn,
		bo:   byteOrder,
	}
}

// Encode writes the byte representation of the provided IMsg to the Unix domain socket,
// including any attached file descriptor via SCM_RIGHTS. ErrLength is returned if the
// resulting imsg would exceed the maximum allowed size. Providing a nil IMsg results in
// a NOOP with nothing written and a nil error returned.
func (enc *UnixEncoder) Encode(im *IMsg) error {
	if im == nil {
		return nil
	}

	wireSize := len(im.Data) + HeaderSizeInBytes
	if wireSize > MaxSizeInBytes {
		return ErrLength
	}

	// Build the message header
	hdr := headerBufferPool.Get().(*headerBuffer)
	defer headerBufferPool.Put(hdr)

	length := uint32(wireSize)
	if im.FD >= 0 {
		length |= imsgFDMark
	}

	enc.bo.PutUint32(hdr[0:], im.Type)
	enc.bo.PutUint32(hdr[4:], length)
	enc.bo.PutUint32(hdr[8:], im.PeerID)
	enc.bo.PutUint32(hdr[12:], im.PID)

	// Build the complete message
	msg := make([]byte, wireSize)
	copy(msg[0:], hdr[:])
	copy(msg[HeaderSizeInBytes:], im.Data)

	// Send with or without FD
	if im.FD >= 0 {
		// Send with file descriptor using SCM_RIGHTS
		oob := unix.UnixRights(im.FD)
		_, _, err := enc.conn.WriteMsgUnix(msg, oob, nil)
		return err
	}

	// Send without file descriptor
	_, err := enc.conn.Write(msg)
	return err
}

// A UnixDecoder reads and decodes IMsgs with file descriptor passing support from a Unix
// domain socket.
type UnixDecoder struct {
	conn *net.UnixConn
	bo   binary.ByteOrder
}

// NewUnixDecoder returns a new Unix decoder that reads from the provided Unix domain socket
// in the current system's byte order. This decoder supports receiving file descriptors via
// SCM_RIGHTS control messages.
func NewUnixDecoder(conn *net.UnixConn) *UnixDecoder {
	return NewUnixDecoderWithByteOrder(conn, binary.NativeEndian)
}

// NewUnixDecoderWithByteOrder returns a new Unix decoder that reads from the provided Unix
// domain socket in the specified byte order.
func NewUnixDecoderWithByteOrder(conn *net.UnixConn, byteOrder binary.ByteOrder) *UnixDecoder {
	return &UnixDecoder{
		conn: conn,
		bo:   byteOrder,
	}
}

// Decode reads the next IMsg from the Unix domain socket and stores it in the value pointed
// to by im. ErrLength is returned if the imsg's size is greater than the maximum allowed size.
// If a file descriptor is passed with the message, it will be stored in im.FD. Otherwise,
// im.FD will be set to -1.
func (dec *UnixDecoder) Decode(im *IMsg) error {
	// Read header first to determine message size
	hdr := headerBufferPool.Get().(*headerBuffer)
	defer headerBufferPool.Put(hdr)

	// Prepare to receive control messages (for FD passing)
	oob := make([]byte, unix.CmsgSpace(4)) // Space for one int (file descriptor)
	buf := make([]byte, MaxSizeInBytes)

	n, oobn, _, _, err := dec.conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return err
	}

	if n < HeaderSizeInBytes {
		return ErrLength
	}

	// Parse header
	copy(hdr[:], buf[0:HeaderSizeInBytes])

	im.Type = dec.bo.Uint32(hdr[0:])
	length := dec.bo.Uint32(hdr[4:])
	im.PeerID = dec.bo.Uint32(hdr[8:])
	im.PID = dec.bo.Uint32(hdr[12:])

	// Check for FD mark and extract actual wire size
	hasFD := (length & imsgFDMark) != 0
	wireSize := int(length &^ imsgFDMark)

	if wireSize > MaxSizeInBytes {
		return ErrLength
	}

	if wireSize > n {
		return ErrLength
	}

	// Extract file descriptor if present
	im.FD = -1
	if hasFD && oobn > 0 {
		scms, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err == nil && len(scms) > 0 {
			fds, err := unix.ParseUnixRights(&scms[0])
			if err == nil && len(fds) > 0 {
				im.FD = fds[0]
				// Close any extra FDs we don't need
				for i := 1; i < len(fds); i++ {
					unix.Close(fds[i])
				}
			}
		}
	}

	// Extract data
	if wireSize > HeaderSizeInBytes {
		im.Data = make([]byte, wireSize-HeaderSizeInBytes)
		copy(im.Data, buf[HeaderSizeInBytes:wireSize])
	} else {
		im.Data = nil
	}

	return nil
}
