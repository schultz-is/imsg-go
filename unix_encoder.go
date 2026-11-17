// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build unix

package imsg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// A UnixEncoder writes imsgs with optional file descriptors to a Unix stream socket.
// Calls to Encode are serialized. Use only one encoder per socket and do not mix
// its writes with other writes to the socket. The caller retains ownership of the
// socket. A UnixEncoder may be used alongside a UnixDecoder on the same socket.
type UnixEncoder struct {
	conn unixMessageWriter
	bo   binary.ByteOrder
	mu   sync.Mutex
	err  error
}

// NewUnixEncoder returns an encoder for a connected Unix stream socket, using the
// current system's byte order. It does not take ownership of conn.
func NewUnixEncoder(conn *net.UnixConn) (*UnixEncoder, error) {
	return NewUnixEncoderWithByteOrder(conn, binary.NativeEndian)
}

// NewUnixEncoderWithByteOrder returns a Unix encoder using the specified header
// byte order. Descriptor control data always uses the host operating system's format.
func NewUnixEncoderWithByteOrder(conn *net.UnixConn, byteOrder binary.ByteOrder) (*UnixEncoder, error) {
	if err := checkUnixConn(conn, byteOrder); err != nil {
		return nil, err
	}
	return &UnixEncoder{conn: unixStreamWriter{conn}, bo: byteOrder}, nil
}

// Encode writes im and optionally sends file with it. A nil file sends no descriptor.
// The file is borrowed and is never closed by Encode. A nil im with a nil file is
// a NOOP; a nil im with a non-nil file is an error.
//
// After a socket write fails, subsequent calls return the same error without writing.
// The peer may have received a descriptor even if sending the full message fails.
// Argument errors do not prevent subsequent calls.
func (enc *UnixEncoder) Encode(im *IMsg, file *os.File) error {
	enc.mu.Lock()
	defer enc.mu.Unlock()
	if enc.err != nil {
		return enc.err
	}
	if im == nil {
		if file != nil {
			return errors.New("imsg: a message is required when sending a file")
		}
		return nil
	}

	h, err := newHeader(*im)
	if err != nil {
		return err
	}
	if file != nil {
		h.length |= imsgFDMark
	}
	b := make([]byte, h.wireSize())
	h.marshal(enc.bo, b)
	copy(b[HeaderSizeInBytes:], im.Data)

	if file == nil {
		return enc.writeFrame(b, nil)
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	// Control keeps the descriptor valid, including against concurrent Close,
	// until the send has finished.
	if err := raw.Control(func(fd uintptr) {
		sendErr = enc.writeFrame(b, unix.UnixRights(int(fd)))
	}); err != nil {
		return err
	}
	return sendErr
}

func (enc *UnixEncoder) writeFrame(b, oob []byte) error {
	for len(b) > 0 {
		chunk := b
		if len(oob) > 0 {
			chunk = b[:min(len(b), HeaderSizeInBytes)]
		}
		n, oobn, err := enc.conn.WriteMsgUnix(chunk, oob, nil)
		if err == nil && oobn != len(oob) {
			err = fmt.Errorf("%w: incomplete descriptor write", ErrFD)
		}
		if err == nil && (n <= 0 || n > len(chunk)) {
			err = io.ErrShortWrite
		}
		if err != nil {
			enc.err = err
			return err
		}
		b = b[n:]
		// A successful send transfers the descriptor with its first bytes.
		// Never attach it again when completing a partial write.
		oob = nil
	}
	return nil
}

type unixStreamWriter struct {
	*net.UnixConn
}

func (w unixStreamWriter) WriteMsgUnix(b, oob []byte, _ *net.UnixAddr) (int, int, error) {
	if len(oob) == 0 {
		n, err := w.Write(b)
		return n, 0, err
	}
	raw, err := w.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var n int
	var sendErr error
	err = raw.Write(func(fd uintptr) bool {
		for {
			n, sendErr = unix.SendmsgN(int(fd), b, oob, nil, unix.MSG_NOSIGNAL)
			if sendErr == unix.EINTR {
				continue
			}
			// Darwin may report EMSGSIZE when pending data leaves too
			// little space for a control message. Wait through Go's
			// poller, preserving socket deadlines, rather than spin.
			return sendErr != unix.EAGAIN && sendErr != unix.EWOULDBLOCK &&
				sendErr != unix.ENOBUFS && sendErr != unix.EMSGSIZE
		}
	})
	if err == nil {
		err = sendErr
	}
	if err != nil {
		return n, 0, &net.OpError{Op: "write", Net: "unix", Source: w.LocalAddr(), Addr: w.RemoteAddr(), Err: err}
	}
	return n, len(oob), nil
}
