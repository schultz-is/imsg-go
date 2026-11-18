// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build unix

package imsg

import (
	"encoding/binary"
	"errors"
	"net"
)

// ErrFD is returned when file descriptors do not match the message header, or when
// descriptor control data is invalid or truncated.
var ErrFD = errors.New("imsg: invalid file descriptor attachment")

type unixMessageWriter interface {
	WriteMsgUnix([]byte, []byte, *net.UnixAddr) (int, int, error)
}

type unixMessageReader interface {
	ReadMsgUnix([]byte, []byte) (int, int, int, *net.UnixAddr, error)
}

func checkUnixConn(conn *net.UnixConn, byteOrder binary.ByteOrder) error {
	if conn == nil || conn.LocalAddr() == nil || conn.RemoteAddr() == nil ||
		conn.LocalAddr().Network() != "unix" || conn.RemoteAddr().Network() != "unix" {
		return errors.New("imsg: a connected Unix stream socket is required")
	}
	if byteOrder == nil {
		return errors.New("imsg: a byte order is required")
	}
	return nil
}
