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

// A UnixDecoder reads imsgs and optional file descriptors from a Unix stream socket.
// Calls to Decode are serialized. Use only one decoder per socket and do not mix
// its reads with other reads from the socket. The caller retains ownership of the
// socket. A UnixDecoder may be used alongside a UnixEncoder on the same socket.
type UnixDecoder struct {
	conn unixMessageReader
	bo   binary.ByteOrder
	mu   sync.Mutex
	err  error
}

// NewUnixDecoder returns a decoder for a connected Unix stream socket, using the
// current system's byte order. It does not take ownership of conn.
func NewUnixDecoder(conn *net.UnixConn) (*UnixDecoder, error) {
	return NewUnixDecoderWithByteOrder(conn, binary.NativeEndian)
}

// NewUnixDecoderWithByteOrder returns a Unix decoder using the specified header
// byte order. Descriptor control data always uses the host operating system's format.
func NewUnixDecoderWithByteOrder(conn *net.UnixConn, byteOrder binary.ByteOrder) (*UnixDecoder, error) {
	if err := checkUnixConn(conn, byteOrder); err != nil {
		return nil, err
	}
	return &UnixDecoder{conn: conn, bo: byteOrder}, nil
}

// Decode reads a message and returns its attached file, or nil if there is none.
// The caller owns the returned file and must close it. Received files are marked
// close-on-exec using Go's platform-specific Unix socket handling.
//
// If an error is returned, im is unchanged and any newly received descriptors are
// closed. io.EOF is returned only between messages; a truncated message returns
// io.ErrUnexpectedEOF. After a read or protocol error, subsequent calls return the
// same error without reading. A nil destination is an argument error and does not
// prevent subsequent calls.
func (dec *UnixDecoder) Decode(im *IMsg) (*os.File, error) {
	dec.mu.Lock()
	defer dec.mu.Unlock()
	if dec.err != nil {
		return nil, dec.err
	}
	if im == nil {
		return nil, errors.New("imsg: a destination message is required")
	}

	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	oob := make([]byte, unix.CmsgSpace(4))
	hdr := headerBufferPool.Get().(*headerBuffer)
	defer headerBufferPool.Put(hdr)

	n, err := dec.readFull(hdr[:], oob, &fds)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.EOF
			if n > 0 || len(fds) > 0 {
				err = io.ErrUnexpectedEOF
			}
		}
		return dec.fail(err)
	}
	h, err := parseHeader(dec.bo, hdr[:])
	if err != nil {
		return dec.fail(err)
	}

	var data []byte
	if h.wireSize() > HeaderSizeInBytes {
		data = make([]byte, h.wireSize()-HeaderSizeInBytes)
		if _, err := dec.readFull(data, oob, &fds); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return dec.fail(err)
		}
	}
	if h.hasFD() != (len(fds) == 1) {
		return dec.fail(fmt.Errorf("%w: descriptor does not match header", ErrFD))
	}
	var file *os.File
	if len(fds) == 1 {
		file = os.NewFile(uintptr(fds[0]), "imsg")
		fds = nil // Ownership passes to file and then to the caller.
	}
	*im = IMsg{Type: h.typ, PeerID: h.peerID, PID: h.pid, Data: data}
	return file, nil
}

func (dec *UnixDecoder) fail(err error) (*os.File, error) {
	dec.err = err
	return nil, err
}

func (dec *UnixDecoder) readFull(b, oob []byte, fds *[]int) (int, error) {
	total := 0
	for total < len(b) {
		n, oobn, flags, _, err := dec.conn.ReadMsgUnix(b[total:], oob)
		received, controlErr := parseRights(oob[:oobn])
		*fds = append(*fds, received...)
		total += n
		if controlErr != nil {
			return total, controlErr
		}
		if flags&unix.MSG_CTRUNC != 0 {
			return total, fmt.Errorf("%w: truncated control data", ErrFD)
		}
		if len(*fds) > 1 {
			return total, fmt.Errorf("%w: received more than one descriptor", ErrFD)
		}
		if err != nil && total < len(b) {
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

// parseRights returns descriptors already parsed even when a later control
// message is malformed, so the caller can close them on every error path.
func parseRights(oob []byte) ([]int, error) {
	var fds []int
	for len(oob) > 0 {
		if len(oob) < unix.CmsgLen(0) {
			return fds, fmt.Errorf("%w: short control header", ErrFD)
		}
		h, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			return fds, fmt.Errorf("%w: malformed control header", ErrFD)
		}
		if h.Level == unix.SOL_SOCKET && h.Type == unix.SCM_RIGHTS {
			// Parse complete integers before rejecting a malformed remainder,
			// so those descriptors are still available for cleanup.
			size := len(data) &^ 3
			// The level/type checks above satisfy ParseUnixRights' error checks.
			rights, _ := unix.ParseUnixRights(&unix.SocketControlMessage{Header: h, Data: data[:size]})
			fds = append(fds, rights...)
			if size == 0 || size != len(data) {
				return fds, fmt.Errorf("%w: malformed descriptor data", ErrFD)
			}
		}
		oob = rest
	}
	return fds, nil
}
