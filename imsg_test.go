// Copyright 2022 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

package imsg

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNew(t *testing.T) {
	validIMsgs := []struct {
		name   string
		typ    uint32
		peerID uint32
		pid    uint32
		data   []byte
		want   *IMsg
	}{
		{
			name: "empty imsg",
			want: &IMsg{PID: uint32(os.Getpid()), FD: -1},
		},
		{
			name: "vmctl get status",
			typ:  25,
			want: &IMsg{
				Type: 25,
				PID:  uint32(os.Getpid()),
				FD:   -1,
			},
		},
		{
			name:   "max-valued fields",
			typ:    ^uint32(0),
			peerID: ^uint32(0),
			pid:    ^uint32(0),
			data:   make([]byte, MaxDataSizeInBytes),
			want: &IMsg{
				Type:   ^uint32(0),
				PeerID: ^uint32(0),
				PID:    ^uint32(0),
				Data:   make([]byte, MaxDataSizeInBytes),
				FD:     -1,
			},
		},
	}

	for _, test := range validIMsgs {
		t.Run(
			test.name,
			func(t *testing.T) {
				out, err := New(test.typ, test.peerID, test.pid, test.data)
				if err != nil {
					t.Fatalf("New() unexpected error: %s", err)
				}
				if out == nil {
					t.Fatal("New() unexpected nil return")
				}
				if !reflect.DeepEqual(test.want, out) {
					t.Fatalf("New() got %#v, want %#v", out, test.want)
				}
			},
		)
	}

	t.Run(
		"data too large",
		func(t *testing.T) {
			out, err := New(0, 0, 0, make([]byte, MaxDataSizeInBytes+1))
			if err == nil || out != nil {
				t.Fatalf("New() unexpected success")
			}
			if err != ErrLength {
				t.Fatalf("New() incorrect error, got %s, want %s", err, ErrLength)
			}
		},
	)
}

func TestMarshalBinary(t *testing.T) {
	validIMsgs := []struct {
		name   string
		in     IMsg
		wantLE string
		wantBE string
	}{
		{
			name:   "empty imsg",
			in:     IMsg{},
			wantLE: "00000000100000000000000000000000",
			wantBE: "00000000001000000000000000000000",
		},
		{
			name: "vmctl get status",
			in: IMsg{
				Type: 25,
				PID:  61804,
			},
			wantLE: "1900000010000000000000006cf10000",
			wantBE: "0000001900100000000000000000f16c",
		},
		{
			name: "max-valued fields",
			in: IMsg{
				Type:   ^uint32(0),
				PeerID: ^uint32(0),
				PID:    ^uint32(0),
				Data:   make([]byte, MaxDataSizeInBytes),
			},
			wantLE: fmt.Sprintf("ffffffff00400000ffffffffffffffff%x", make([]byte, MaxDataSizeInBytes)),
			wantBE: fmt.Sprintf("ffffffff40000000ffffffffffffffff%x", make([]byte, MaxDataSizeInBytes)),
		},
		{
			name: "ancillary data",
			in: IMsg{
				Type:   1234,
				PeerID: 888,
				PID:    61804,
				Data:   []byte("Howdy y'all!"),
			},
			wantLE: "d20400001c000000780300006cf10000486f776479207927616c6c21",
			wantBE: "000004d2001c0000000003780000f16c486f776479207927616c6c21",
		},
	}

	for _, test := range validIMsgs {
		for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			t.Run(
				test.name,
				func(t *testing.T) {
					var out []byte
					var err error

					if bo == binary.NativeEndian {
						out, err = test.in.MarshalBinary()
					} else {
						out, err = test.in.MarshalBinaryWithByteOrder(bo)
					}
					if err != nil {
						t.Fatalf("MarshalBinary() (%s) unexpected err: %s", bo, err)
					}

					var want string
					if bo == binary.LittleEndian {
						want = test.wantLE
					} else {
						want = test.wantBE
					}

					outStr := hex.EncodeToString(out)
					if outStr != want {
						t.Fatalf("MarshalBinary() (%s) got %s, want %s", bo, outStr, want)
					}
				},
			)
		}
	}

	t.Run(
		"imsg too large",
		func(t *testing.T) {
			bs, err := IMsg{Data: make([]byte, MaxDataSizeInBytes+1)}.MarshalBinary()
			if err == nil || len(bs) > 0 {
				t.Fatal("MarshalBinary() unexpected success")
			}
			if err != ErrLength {
				t.Fatalf("MarshalBinary() incorrect error, got %s, want %s", err, ErrLength)
			}
		},
	)
}

func TestUnmarshalBinary(t *testing.T) {
	validIMsgs := []struct {
		name string
		inLE []byte
		inBE []byte
		want IMsg
	}{
		{
			name: "empty imsg",
			inLE: mustDecodeHexString(t, "00000000100000000000000000000000"),
			inBE: mustDecodeHexString(t, "00000000001000000000000000000000"),
			want: IMsg{},
		},
		{
			name: "vmctl get status",
			inLE: mustDecodeHexString(t, "1900000010000000000000006cf10000"),
			inBE: mustDecodeHexString(t, "0000001900100000000000000000f16c"),
			want: IMsg{
				Type: 25,
				PID:  61804,
			},
		},
		{
			name: "max-valued fields",
			inLE: mustDecodeHexString(t, "ffffffff00400000ffffffffffffffff"+strings.Repeat("00", MaxDataSizeInBytes)),
			inBE: mustDecodeHexString(t, "ffffffff40000000ffffffffffffffff"+strings.Repeat("00", MaxDataSizeInBytes)),
			want: IMsg{
				Type:   ^uint32(0),
				PeerID: ^uint32(0),
				PID:    ^uint32(0),
				Data:   make([]byte, MaxDataSizeInBytes),
			},
		},
		{
			name: "ancillary data",
			inLE: mustDecodeHexString(t, "d20400001c000000780300006cf10000486f776479207927616c6c21"),
			inBE: mustDecodeHexString(t, "000004d2001c0000000003780000f16c486f776479207927616c6c21"),
			want: IMsg{
				Type:   1234,
				PeerID: 888,
				PID:    61804,
				Data:   []byte("Howdy y'all!"),
			},
		},
		{
			name: "flags are ignored",
			inLE: mustDecodeHexString(t, "000000001000beef0000000000000000"),
			inBE: mustDecodeHexString(t, "000000000010beef0000000000000000"),
			want: IMsg{},
		},
		{
			name: "trailing data is ignored",
			inLE: mustDecodeHexString(t, "d204000015000000780300006cf10000486f776479207927616c6c21"),
			inBE: mustDecodeHexString(t, "000004d200150000000003780000f16c486f776479207927616c6c21"),
			want: IMsg{
				Type:   1234,
				PeerID: 888,
				PID:    61804,
				Data:   []byte("Howdy"),
			},
		},
	}

	for _, test := range validIMsgs {
		for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			t.Run(
				test.name,
				func(t *testing.T) {
					var in []byte
					var out IMsg
					var err error

					if bo == binary.LittleEndian {
						in = test.inLE
					} else {
						in = test.inBE
					}

					if bo == binary.NativeEndian {
						err = out.UnmarshalBinary(in)
					} else {
						err = out.UnmarshalBinaryWithByteOrder(bo, in)
					}
					if err != nil {
						t.Fatalf("UnmarshalBinary() (%s) unexpected err: %s", bo, err)
					}

					if !reflect.DeepEqual(out, test.want) {
						t.Fatalf("UnmarshalBinary() (%s) got %#v, want %#v", bo, out, test.want)
					}
				},
			)
		}
	}

	invalidIMsgs := []struct {
		name      string
		in        []byte
		wantError error
	}{
		{
			name:      "nil data",
			in:        nil,
			wantError: ErrLength,
		},
		{
			name:      "empty data",
			in:        []byte{},
			wantError: ErrLength,
		},
		{
			name:      "data too large",
			in:        mustDecodeHexString(t, "ffffffff01400000ffffffffffffffff"+strings.Repeat("00", MaxDataSizeInBytes+1)),
			wantError: ErrLength,
		},
		{
			name:      "data length mismatch",
			in:        mustDecodeHexString(t, "ffffffff1100beef0000000000000000"),
			wantError: ErrLength,
		},
	}

	for _, test := range invalidIMsgs {
		t.Run(
			test.name,
			func(t *testing.T) {
				var out IMsg

				err := out.UnmarshalBinaryWithByteOrder(binary.LittleEndian, test.in)
				if err == nil {
					t.Fatal("UnmarshalBinary() unexpected success")
				}

				if err != test.wantError {
					t.Fatalf("UnmarshalBinary() incorrect error, got %s, want %s", err, test.wantError)
				}
			},
		)
	}
}

func TestEncode(t *testing.T) {
	validIMsgs := []struct {
		name   string
		in     *IMsg
		wantLE string
		wantBE string
	}{
		{
			name:   "nil imsg",
			in:     nil,
			wantLE: "",
			wantBE: "",
		},
		{
			name:   "empty imsg",
			in:     &IMsg{},
			wantLE: "00000000100000000000000000000000",
			wantBE: "00000000001000000000000000000000",
		},
		{
			name: "vmctl get status",
			in: &IMsg{
				Type: 25,
				PID:  61804,
			},
			wantLE: "1900000010000000000000006cf10000",
			wantBE: "0000001900100000000000000000f16c",
		},
		{
			name: "max-valued fields",
			in: &IMsg{
				Type:   ^uint32(0),
				PeerID: ^uint32(0),
				PID:    ^uint32(0),
				Data:   make([]byte, MaxDataSizeInBytes),
			},
			wantLE: fmt.Sprintf("ffffffff00400000ffffffffffffffff%x", make([]byte, MaxDataSizeInBytes)),
			wantBE: fmt.Sprintf("ffffffff40000000ffffffffffffffff%x", make([]byte, MaxDataSizeInBytes)),
		},
		{
			name: "ancillary data",
			in: &IMsg{
				Type:   1234,
				PeerID: 888,
				PID:    61804,
				Data:   []byte("Howdy y'all!"),
			},
			wantLE: "d20400001c000000780300006cf10000486f776479207927616c6c21",
			wantBE: "000004d2001c0000000003780000f16c486f776479207927616c6c21",
		},
	}

	for _, test := range validIMsgs {
		for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			t.Run(
				test.name,
				func(t *testing.T) {
					var outBuf bytes.Buffer
					var err error

					if bo == binary.NativeEndian {
						err = NewEncoder(&outBuf).Encode(test.in)
					} else {
						err = NewEncoderWithByteOrder(&outBuf, bo).Encode(test.in)
					}
					if err != nil {
						t.Fatalf("Encode() (%s) unexpected err: %s", bo, err)
					}

					var want string
					if bo == binary.LittleEndian {
						want = test.wantLE
					} else {
						want = test.wantBE
					}

					out := hex.EncodeToString(outBuf.Bytes())
					if out != want {
						t.Fatalf("Encode() (%s) got %s, want %s", bo, out, want)
					}
				},
			)
		}
	}

	t.Run(
		"imsg too large",
		func(t *testing.T) {
			var outBuf bytes.Buffer
			err := NewEncoder(&outBuf).Encode(&IMsg{Data: make([]byte, MaxDataSizeInBytes+1)})
			if err == nil || outBuf.Len() > 0 {
				t.Fatal("Encode() unexpected success")
			}
			if err != ErrLength {
				t.Fatalf("Encode() incorrect error, got %s, want %s", err, ErrLength)
			}
		},
	)

	t.Run(
		"write error in header",
		func(t *testing.T) {
			err := NewEncoder(newLimitedWriter(io.Discard, HeaderSizeInBytes-1)).Encode(&IMsg{Data: []byte("Howdy y'all!")})
			if err == nil {
				t.Fatal("Encode() unexpected success")
			}
			if err != errLimitEncountered {
				t.Fatalf("Encode() incorrect error, got %s, want %s", err, errLimitEncountered)
			}
		},
	)

	t.Run(
		"write error in data",
		func(t *testing.T) {
			err := NewEncoder(newLimitedWriter(io.Discard, HeaderSizeInBytes+1)).Encode(&IMsg{Data: []byte("Howdy y'all!")})
			if err == nil {
				t.Fatal("Encode() unexpected success")
			}
			if err != errLimitEncountered {
				t.Fatalf("Encode() incorrect error, got %s, want %s", err, errLimitEncountered)
			}
		},
	)
}

func TestDecode(t *testing.T) {
	validIMsgs := []struct {
		name string
		inLE []byte
		inBE []byte
		want IMsg
	}{
		{
			name: "empty imsg",
			inLE: mustDecodeHexString(t, "00000000100000000000000000000000"),
			inBE: mustDecodeHexString(t, "00000000001000000000000000000000"),
			want: IMsg{},
		},
		{
			name: "vmctl get status",
			inLE: mustDecodeHexString(t, "1900000010000000000000006cf10000"),
			inBE: mustDecodeHexString(t, "0000001900100000000000000000f16c"),
			want: IMsg{
				Type: 25,
				PID:  61804,
			},
		},
		{
			name: "max-valued fields",
			inLE: mustDecodeHexString(t, "ffffffff00400000ffffffffffffffff"+strings.Repeat("00", MaxDataSizeInBytes)),
			inBE: mustDecodeHexString(t, "ffffffff40000000ffffffffffffffff"+strings.Repeat("00", MaxDataSizeInBytes)),
			want: IMsg{
				Type:   ^uint32(0),
				PeerID: ^uint32(0),
				PID:    ^uint32(0),
				Data:   make([]byte, MaxDataSizeInBytes),
			},
		},
		{
			name: "ancillary data",
			inLE: mustDecodeHexString(t, "d20400001c000000780300006cf10000486f776479207927616c6c21"),
			inBE: mustDecodeHexString(t, "000004d2001c0000000003780000f16c486f776479207927616c6c21"),
			want: IMsg{
				Type:   1234,
				PeerID: 888,
				PID:    61804,
				Data:   []byte("Howdy y'all!"),
			},
		},
		{
			name: "flags are ignored",
			inLE: mustDecodeHexString(t, "000000001000beef0000000000000000"),
			inBE: mustDecodeHexString(t, "000000000010beef0000000000000000"),
			want: IMsg{},
		},
		{
			name: "trailing data is ignored",
			inLE: mustDecodeHexString(t, "d204000015000000780300006cf10000486f776479207927616c6c21"),
			inBE: mustDecodeHexString(t, "000004d200150000000003780000f16c486f776479207927616c6c21"),
			want: IMsg{
				Type:   1234,
				PeerID: 888,
				PID:    61804,
				Data:   []byte("Howdy"),
			},
		},
	}

	for _, test := range validIMsgs {
		for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			t.Run(
				test.name,
				func(t *testing.T) {
					var inR *bytes.Reader
					var out IMsg
					var err error

					if bo == binary.LittleEndian {
						inR = bytes.NewReader(test.inLE)
					} else {
						inR = bytes.NewReader(test.inBE)
					}

					if bo == binary.NativeEndian {
						err = NewDecoder(inR).Decode(&out)
					} else {
						err = NewDecoderWithByteOrder(inR, bo).Decode(&out)
					}
					if err != nil {
						t.Fatalf("Decode() (%) unexpected err: %s", bo, err)
					}

					if !reflect.DeepEqual(out, test.want) {
						t.Fatalf("Decode() (%s) got %#v, want %#v", bo, out, test.want)
					}
				},
			)
		}
	}

	invalidIMsgs := []struct {
		name      string
		in        []byte
		wantError error
	}{
		{
			name:      "nil data",
			in:        nil,
			wantError: io.EOF,
		},
		{
			name:      "empty data",
			in:        []byte{},
			wantError: io.EOF,
		},
		{
			name:      "data too large",
			in:        mustDecodeHexString(t, "ffffffff01400000ffffffffffffffff"+strings.Repeat("00", MaxDataSizeInBytes+1)),
			wantError: ErrLength,
		},
		{
			name:      "data length mismatch",
			in:        mustDecodeHexString(t, "ffffffff1100beef0000000000000000"),
			wantError: io.EOF,
		},
	}

	for _, test := range invalidIMsgs {
		t.Run(
			test.name,
			func(t *testing.T) {
				var out IMsg

				err := NewDecoder(bytes.NewReader(test.in)).Decode(&out)
				if err == nil {
					t.Fatal("Decode() unexpected success")
				}

				if err != test.wantError {
					t.Fatalf("Decode() incorrect error, got %s, want %s", err, test.wantError)
				}
			},
		)
	}
}

func mustDecodeHexString(t testing.TB, s string) []byte {
	t.Helper()
	bs, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("mustDecodeHexString() unexpected error: %s", err)
	}
	return bs
}

var errLimitEncountered = errors.New("writer limit encountered")

type limitedWriter struct {
	n     int
	limit int
	w     io.Writer
}

func newLimitedWriter(w io.Writer, limit int) io.Writer {
	return &limitedWriter{
		limit: limit,
		w:     w,
	}
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if (lw.limit - lw.n) < len(p) {
		n, err := lw.w.Write(p[:(lw.limit - lw.n)])
		lw.n += n
		if err != nil {
			return n, err
		}
		return n, errLimitEncountered
	}

	n, err := lw.w.Write(p)
	lw.n += n
	return n, err
}

func TestNewWithFD(t *testing.T) {
	t.Run("with valid FD", func(t *testing.T) {
		im, err := NewWithFD(123, 456, 0, []byte("test"), 42)
		if err != nil {
			t.Fatalf("NewWithFD() unexpected error: %s", err)
		}
		if im.FD != 42 {
			t.Fatalf("NewWithFD() FD got %d, want 42", im.FD)
		}
		if im.Type != 123 {
			t.Fatalf("NewWithFD() Type got %d, want 123", im.Type)
		}
	})

	t.Run("with no FD", func(t *testing.T) {
		im, err := NewWithFD(123, 456, 0, []byte("test"), -1)
		if err != nil {
			t.Fatalf("NewWithFD() unexpected error: %s", err)
		}
		if im.FD != -1 {
			t.Fatalf("NewWithFD() FD got %d, want -1", im.FD)
		}
	})

	t.Run("New initializes FD to -1", func(t *testing.T) {
		im, err := New(123, 456, 0, []byte("test"))
		if err != nil {
			t.Fatalf("New() unexpected error: %s", err)
		}
		if im.FD != -1 {
			t.Fatalf("New() FD got %d, want -1", im.FD)
		}
	})
}

func TestUnixEncoderDecoder(t *testing.T) {
	// Skip on systems that don't support Unix domain sockets
	if os.Getenv("GOOS") == "windows" {
		t.Skip("Unix domain sockets not supported on Windows")
	}

	t.Run("encode and decode without FD", func(t *testing.T) {
		client, server := createSocketPair(t)
		defer client.Close()
		defer server.Close()

		encoder := NewUnixEncoder(client)
		decoder := NewUnixDecoder(server)

		sent := &IMsg{
			Type:   777,
			PeerID: 888,
			PID:    999,
			Data:   []byte("Hello from Unix socket!"),
			FD:     -1,
		}

		err := encoder.Encode(sent)
		if err != nil {
			t.Fatalf("Encode() unexpected error: %s", err)
		}

		var received IMsg
		err = decoder.Decode(&received)
		if err != nil {
			t.Fatalf("Decode() unexpected error: %s", err)
		}

		if received.Type != sent.Type {
			t.Errorf("Type: got %d, want %d", received.Type, sent.Type)
		}
		if received.PeerID != sent.PeerID {
			t.Errorf("PeerID: got %d, want %d", received.PeerID, sent.PeerID)
		}
		if received.PID != sent.PID {
			t.Errorf("PID: got %d, want %d", received.PID, sent.PID)
		}
		if !bytes.Equal(received.Data, sent.Data) {
			t.Errorf("Data: got %s, want %s", received.Data, sent.Data)
		}
		if received.FD != -1 {
			t.Errorf("FD: got %d, want -1", received.FD)
		}
	})

	t.Run("encode and decode with FD", func(t *testing.T) {
		client, server := createSocketPair(t)
		defer client.Close()
		defer server.Close()

		// Create a pipe to use as a test FD
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe() failed: %s", err)
		}
		defer r.Close()

		testFD := int(w.Fd())

		encoder := NewUnixEncoder(client)
		decoder := NewUnixDecoder(server)

		sent := &IMsg{
			Type:   777,
			PeerID: 888,
			PID:    999,
			Data:   []byte("Message with FD"),
			FD:     testFD,
		}

		err = encoder.Encode(sent)
		if err != nil {
			t.Fatalf("Encode() unexpected error: %s", err)
		}

		// Close the write end after sending
		w.Close()

		var received IMsg
		err = decoder.Decode(&received)
		if err != nil {
			t.Fatalf("Decode() unexpected error: %s", err)
		}

		if received.Type != sent.Type {
			t.Errorf("Type: got %d, want %d", received.Type, sent.Type)
		}
		if received.FD < 0 {
			t.Fatalf("FD: got %d, expected valid FD", received.FD)
		}

		// Verify the received FD is valid by writing to it
		receivedFile := os.NewFile(uintptr(received.FD), "received")
		defer receivedFile.Close()

		testData := []byte("test write")
		_, err = receivedFile.Write(testData)
		if err != nil {
			t.Fatalf("Write to received FD failed: %s", err)
		}

		// Read from the read end of the pipe to verify the FD works
		readBuf := make([]byte, len(testData))
		n, err := r.Read(readBuf)
		if err != nil {
			t.Fatalf("Read from pipe failed: %s", err)
		}
		if n != len(testData) {
			t.Fatalf("Read %d bytes, expected %d", n, len(testData))
		}
		if !bytes.Equal(readBuf, testData) {
			t.Fatalf("Read data mismatch: got %s, want %s", readBuf, testData)
		}
	})

	t.Run("encode nil IMsg", func(t *testing.T) {
		client, server := createSocketPair(t)
		defer client.Close()
		defer server.Close()

		encoder := NewUnixEncoder(client)
		err := encoder.Encode(nil)
		if err != nil {
			t.Fatalf("Encode(nil) unexpected error: %s", err)
		}
	})

	t.Run("message too large", func(t *testing.T) {
		client, server := createSocketPair(t)
		defer client.Close()
		defer server.Close()

		encoder := NewUnixEncoder(client)
		err := encoder.Encode(&IMsg{Data: make([]byte, MaxDataSizeInBytes+1)})
		if err != ErrLength {
			t.Fatalf("Encode() error: got %v, want %v", err, ErrLength)
		}
	})
}

// createSocketPair creates a connected pair of Unix domain sockets for testing
func createSocketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("unix.Socketpair() failed: %s", err)
	}

	f1 := os.NewFile(uintptr(fds[0]), "socket1")
	f2 := os.NewFile(uintptr(fds[1]), "socket2")

	c1, err := net.FileConn(f1)
	if err != nil {
		t.Fatalf("net.FileConn() failed: %s", err)
	}
	f1.Close() // FileConn dups the FD, so we can close the original

	c2, err := net.FileConn(f2)
	if err != nil {
		t.Fatalf("net.FileConn() failed: %s", err)
	}
	f2.Close()

	return c1.(*net.UnixConn), c2.(*net.UnixConn)
}
