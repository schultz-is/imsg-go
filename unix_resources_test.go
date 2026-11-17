// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build unix

package imsg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUnixPipeAndSocket(t *testing.T) {
	for _, kind := range []string{"pipe", "socket"} {
		t.Run(kind, func(t *testing.T) {
			left, right := unixPair(t)
			enc, err := NewUnixEncoder(left)
			if err != nil {
				t.Fatal(err)
			}
			dec, err := NewUnixDecoder(right)
			if err != nil {
				t.Fatal(err)
			}
			var resource *os.File
			var writer io.Writer
			if kind == "pipe" {
				var output *os.File
				resource, output, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = output.Close() })
				writer = output
			} else {
				resourceConn, output := unixPair(t)
				resource, err = resourceConn.File()
				if err != nil {
					t.Fatal(err)
				}
				writer = output
			}
			t.Cleanup(func() { _ = resource.Close() })
			sent := make(chan error, 1)
			go func() { sent <- enc.Encode(&IMsg{Type: 1}, resource) }()
			var im IMsg
			received, err := dec.Decode(&im)
			if err != nil || received == nil {
				t.Fatalf("Decode() got file %v, error %v", received, err)
			}
			t.Cleanup(func() { _ = received.Close() })
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			if err := resource.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 5)
			if _, err := io.ReadFull(received, b); err != nil || string(b) != "hello" {
				t.Fatalf("received resource got %q, error %v", b, err)
			}
		})
	}
}

type recordingUnixReader struct {
	unixMessageReader
	fds []int
}

func (r *recordingUnixReader) ReadMsgUnix(b, oob []byte) (int, int, int, *net.UnixAddr, error) {
	n, oobn, flags, addr, err := r.unixMessageReader.ReadMsgUnix(b, oob)
	fds, _ := parseRights(oob[:oobn])
	r.fds = append(r.fds, fds...)
	return n, oobn, flags, addr, err
}

func assertDescriptorsClosed(t *testing.T, fds []int) {
	t.Helper()
	if len(fds) == 0 {
		t.Fatal("test did not receive a descriptor")
	}
	for _, fd := range fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Errorf("descriptor %d was not closed: %v", fd, err)
			_ = unix.Close(fd)
		}
	}
}

func TestUnixReceivedDescriptorCleanup(t *testing.T) {
	for _, name := range []string{"unexpected", "truncated", "deadline"} {
		t.Run(name, func(t *testing.T) {
			left, right := unixPair(t)
			dec, err := NewUnixDecoder(right)
			if err != nil {
				t.Fatal(err)
			}
			recorder := &recordingUnixReader{unixMessageReader: right}
			dec.conn = recorder
			source := testFile(t)
			h := make([]byte, HeaderSizeInBytes)
			length := uint32(HeaderSizeInBytes) | imsgFDMark
			fds := []int{int(source.Fd())}
			wantErr := ErrFD
			switch name {
			case "unexpected":
				length &^= imsgFDMark
			case "truncated":
				fds = append(fds, fds[0], fds[0])
			case "deadline":
				length++
				wantErr = os.ErrDeadlineExceeded
				if err := right.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
			}
			binary.NativeEndian.PutUint32(h[4:], length)
			if _, _, err := left.WriteMsgUnix(h, unix.UnixRights(fds...), nil); err != nil {
				t.Fatal(err)
			}
			want := IMsg{Type: 1, Data: []byte("previous")}
			got := want
			got.Data = bytes.Clone(want.Data)
			if file, err := dec.Decode(&got); file != nil || !errors.Is(err, wantErr) {
				t.Fatalf("Decode() got file %v, error %v; want %v", file, err, wantErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("failed Decode() changed destination")
			}
			// Darwin discards all rights on truncation; Linux may deliver
			// the descriptors that fit. Close every descriptor delivered.
			if name != "truncated" || len(recorder.fds) > 0 {
				assertDescriptorsClosed(t, recorder.fds)
			}
			checkFile(t, source)
		})
	}
}

func TestUnixFDZero(t *testing.T) {
	if os.Getenv("IMSG_TEST_FD_ZERO") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUnixFDZero$")
		cmd.Env = append(os.Environ(), "IMSG_TEST_FD_ZERO=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("FD zero subprocess: %v\n%s", err, output)
		}
		return
	}
	source := testFile(t)
	if err := unix.Dup2(int(source.Fd()), 0); err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(0, "descriptor zero")
	t.Cleanup(func() { _ = file.Close() })
	left, right := unixPair(t)
	enc, err := NewUnixEncoder(left)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewUnixDecoder(right)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { sent <- enc.Encode(&IMsg{}, file) }()
	received, err := dec.Decode(&IMsg{})
	if err != nil || received == nil {
		t.Fatalf("FD zero Decode() got file %v, error %v", received, err)
	}
	defer func() { _ = received.Close() }()
	checkFile(t, received)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestUnixRejectDatagram(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "datagram")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, err := NewUnixEncoder(conn.(*net.UnixConn)); err == nil {
			t.Fatal("datagram socket accepted by encoder")
		}
		if _, err := NewUnixDecoder(conn.(*net.UnixConn)); err == nil {
			t.Fatal("datagram socket accepted by decoder")
		}
	}
}

func TestUnixWriteFailures(t *testing.T) {
	for _, name := range []string{"closed peer", "deadline"} {
		t.Run(name, func(t *testing.T) {
			left, right := unixPair(t)
			enc, err := NewUnixEncoder(left)
			if err != nil {
				t.Fatal(err)
			}
			message := IMsg{}
			if name == "closed peer" {
				if err := right.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := left.SetWriteBuffer(1024); err != nil {
					t.Fatal(err)
				}
				if err := left.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				message.Data = make([]byte, MaxDataSizeInBytes)
			}
			file := testFile(t)
			err = enc.Encode(&message, file)
			if err == nil || (name == "deadline" && !errors.Is(err, os.ErrDeadlineExceeded)) {
				t.Fatalf("Encode() got error %v", err)
			}
			if again := enc.Encode(&IMsg{}, nil); again != err {
				t.Fatal("failed encoder did not retain its error")
			}
			checkFile(t, file)
		})
	}
}

func FuzzParseRights(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1})
	f.Add(unix.UnixRights(0))
	f.Add(append(unix.UnixRights(0), 1))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = parseRights(b)
	})
}
