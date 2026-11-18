// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build unix

package imsg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func unixPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns [2]*net.UnixConn
	for i, fd := range fds {
		unix.CloseOnExec(fd)
		file := os.NewFile(uintptr(fd), "socketpair")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = conn.(*net.UnixConn)
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return conns[0], conns[1]
}

func testFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "resource")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.WriteAt([]byte("passed resource"), 0); err != nil {
		t.Fatal(err)
	}
	return file
}

func checkFile(t *testing.T, file *os.File) {
	t.Helper()
	b := make([]byte, len("passed resource"))
	if _, err := file.ReadAt(b, 0); err != nil || string(b) != "passed resource" {
		t.Fatalf("received file got %q, error %v", b, err)
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("received descriptor is not close-on-exec: flags=%d, error=%v", flags, err)
	}
}

func TestUnixRoundTrip(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian, binary.NativeEndian} {
		t.Run(bo.String(), func(t *testing.T) {
			left, right := unixPair(t)
			var enc *UnixEncoder
			var dec *UnixDecoder
			var err error
			if bo == binary.NativeEndian {
				enc, err = NewUnixEncoder(left)
				if err == nil {
					dec, err = NewUnixDecoder(right)
				}
			} else {
				enc, err = NewUnixEncoderWithByteOrder(left, bo)
				if err == nil {
					dec, err = NewUnixDecoderWithByteOrder(right, bo)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			file := testFile(t)
			messages := []IMsg{
				{Type: 1, PeerID: 2, PID: 3, Data: []byte("with file")},
				{Type: 2},
				{Type: 3},
				{Type: 4, Data: make([]byte, MaxDataSizeInBytes)},
				{Type: 5, Data: []byte("another file")},
				{Type: 7, Data: make([]byte, MaxDataSizeInBytes)},
			}
			done := make(chan error, 1)
			go func() {
				defer func() { _ = left.Close() }()
				for _, im := range messages {
					var attached *os.File
					if im.Type%2 == 1 {
						attached = file
					}
					if err := enc.Encode(&im, attached); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			var got IMsg
			for _, want := range messages {
				received, err := dec.Decode(&got)
				if err != nil {
					select {
					case sendErr := <-done:
						t.Fatalf("Decode() message %d: %v; sender: %v", want.Type, err, sendErr)
					default:
						t.Fatalf("Decode() message %d: %v", want.Type, err)
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Decode() got %#v, want %#v", got, want)
				}
				if (received != nil) != (want.Type%2 == 1) {
					t.Fatalf("Decode() incorrect descriptor for message %d", want.Type)
				}
				if received != nil {
					checkFile(t, received)
					if err := received.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := got
			if received, err := dec.Decode(&got); received != nil || err != io.EOF {
				t.Fatalf("Decode() at end got file %v, error %v", received, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("EOF modified the destination")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			checkFile(t, file)
		})
	}
}

type fragmentUnixReader struct {
	unixMessageReader
	size int
}

func (r fragmentUnixReader) ReadMsgUnix(b, oob []byte) (int, int, int, *net.UnixAddr, error) {
	if len(b) > r.size {
		b = b[:r.size]
	}
	return r.unixMessageReader.ReadMsgUnix(b, oob)
}

func TestUnixFragmentedAndCoalesced(t *testing.T) {
	left, right := unixPair(t)
	dec, err := NewUnixDecoder(right)
	if err != nil {
		t.Fatal(err)
	}
	dec.conn = fragmentUnixReader{right, 1}
	file := testFile(t)
	first := IMsg{Type: 1, Data: []byte("fragmented")}
	second := IMsg{Type: 2, Data: []byte("coalesced")}
	b1, _ := first.MarshalBinary()
	b2, _ := second.MarshalBinary()
	binary.NativeEndian.PutUint32(b1[4:], uint32(len(b1))|imsgFDMark)
	done := make(chan error, 1)
	go func() {
		_, _, err := left.WriteMsgUnix(b1[:8], nil, nil)
		if err == nil {
			_, _, err = left.WriteMsgUnix(append(b1[8:], b2...), unix.UnixRights(int(file.Fd())), nil)
		}
		done <- err
	}()
	for _, want := range []IMsg{first, second} {
		var got IMsg
		received, err := dec.Decode(&got)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || (received != nil) != (want.Type == 1) {
			t.Fatalf("Decode() got %#v, file %v; want %#v", got, received, want)
		}
		if received != nil {
			checkFile(t, received)
			_ = received.Close()
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type unixReadStep struct {
	b, oob []byte
	flags  int
	err    error
}

type scriptedUnixReader struct {
	steps []unixReadStep
	calls int
}

func (r *scriptedUnixReader) ReadMsgUnix(b, oob []byte) (int, int, int, *net.UnixAddr, error) {
	r.calls++
	if len(r.steps) == 0 {
		return 0, 0, 0, nil, io.EOF
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	if len(step.b) > len(b) || len(step.oob) > len(oob) {
		panic("test step exceeds read buffers")
	}
	return copy(b, step.b), copy(oob, step.oob), step.flags, nil, step.err
}

func TestUnixDecodeErrors(t *testing.T) {
	readErr := errors.New("read failed")
	for _, name := range []string{
		"EOF", "short header", "invalid length", "missing descriptor",
		"unexpected descriptor", "extra descriptors", "truncated control",
		"malformed control", "missing payload", "short payload", "read error", "no progress",
	} {
		t.Run(name, func(t *testing.T) {
			source := testFile(t)
			var owned []int
			rights := func() []byte {
				fd, err := unix.Dup(int(source.Fd()))
				if err != nil {
					t.Fatal(err)
				}
				owned = append(owned, fd)
				return unix.UnixRights(fd)
			}
			h := make([]byte, HeaderSizeInBytes)
			binary.NativeEndian.PutUint32(h[4:], HeaderSizeInBytes|imsgFDMark)
			r := &scriptedUnixReader{}
			wantErr := ErrFD
			switch name {
			case "EOF":
				r.steps = []unixReadStep{{err: io.EOF}}
				wantErr = io.EOF
			case "short header":
				r.steps = []unixReadStep{{b: h[:8], oob: rights(), err: io.EOF}}
				wantErr = io.ErrUnexpectedEOF
			case "invalid length":
				binary.NativeEndian.PutUint32(h[4:], imsgFDMark)
				r.steps = []unixReadStep{{b: h, oob: rights()}}
				wantErr = ErrLength
			case "missing descriptor":
				r.steps = []unixReadStep{{b: h}}
			case "unexpected descriptor":
				binary.NativeEndian.PutUint32(h[4:], HeaderSizeInBytes)
				r.steps = []unixReadStep{{b: h, oob: rights()}}
			case "extra descriptors":
				r.steps = []unixReadStep{{b: h[:8], oob: rights()}, {b: h[8:], oob: rights()}}
			case "truncated control":
				r.steps = []unixReadStep{{b: h, oob: rights(), flags: unix.MSG_CTRUNC}}
			case "malformed control":
				r.steps = []unixReadStep{{b: h[:8], oob: rights()}, {b: h[8:], oob: []byte{1}}}
			case "missing payload", "short payload", "read error":
				binary.NativeEndian.PutUint32(h[4:], (HeaderSizeInBytes+2)|imsgFDMark)
				last := unixReadStep{err: io.EOF}
				wantErr = io.ErrUnexpectedEOF
				if name != "missing payload" {
					last.b = []byte{1}
				}
				if name == "read error" {
					last.err, wantErr = readErr, readErr
				}
				r.steps = []unixReadStep{{b: h, oob: rights()}, last}
			case "no progress":
				r.steps = []unixReadStep{{}}
				wantErr = io.ErrNoProgress
			}
			dec := &UnixDecoder{conn: r, bo: binary.NativeEndian}
			want := IMsg{Type: 7, PeerID: 8, PID: 9, Data: []byte("previous")}
			got := want
			got.Data = bytes.Clone(want.Data)
			received, err := dec.Decode(&got)
			if received != nil || !errors.Is(err, wantErr) {
				t.Fatalf("Decode() got file %v, error %v; want %v", received, err, wantErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Decode() modified destination: got %#v, want %#v", got, want)
			}
			for _, fd := range owned {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
					t.Errorf("descriptor %d was not closed: %v", fd, err)
					_ = unix.Close(fd)
				}
			}
			calls := r.calls
			if file, again := dec.Decode(&got); file != nil || again != err || r.calls != calls {
				t.Fatal("failed decoder read again or changed its error")
			}
		})
	}
}

func TestUnixDecodeCompleteRead(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("payload")} {
		for _, readErr := range []error{io.EOF, errors.New("read failed")} {
			t.Run(fmt.Sprintf("%d bytes/%v", len(payload), readErr), func(t *testing.T) {
				want := IMsg{Type: 1, Data: payload}
				b, err := want.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				steps := []unixReadStep{{b: b[:HeaderSizeInBytes], err: readErr}}
				if len(payload) > 0 {
					steps[0].err = nil
					steps = append(steps, unixReadStep{b: b[HeaderSizeInBytes:], err: readErr})
				}
				dec := &UnixDecoder{conn: &scriptedUnixReader{steps: steps}, bo: binary.NativeEndian}
				var got IMsg
				if file, err := dec.Decode(&got); file != nil || err != nil {
					t.Fatalf("complete message got file %v, error %v", file, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Decode() got %#v, want %#v", got, want)
				}
			})
		}
	}
}

type chunkUnixWriter struct {
	b          bytes.Buffer
	oobs       [][]byte
	chunk      int
	failAt     int
	err        error
	shortOOB   bool
	noProgress bool
}

func (w *chunkUnixWriter) WriteMsgUnix(b, oob []byte, _ *net.UnixAddr) (int, int, error) {
	w.oobs = append(w.oobs, bytes.Clone(oob))
	n := min(len(b), w.chunk)
	if w.noProgress {
		n = 0
	}
	_, _ = w.b.Write(b[:n])
	oobn := len(oob)
	if w.shortOOB {
		oobn = 0
	}
	if w.failAt == len(w.oobs) {
		return n, oobn, w.err
	}
	return n, oobn, nil
}

func TestUnixEncodePartialWrites(t *testing.T) {
	file := testFile(t)
	im := IMsg{Type: 7, PeerID: 8, PID: 9, Data: []byte("payload")}
	w := &chunkUnixWriter{chunk: 3}
	enc := &UnixEncoder{conn: w, bo: binary.NativeEndian}
	if err := enc.Encode(&im, file); err != nil {
		t.Fatal(err)
	}
	want, _ := im.MarshalBinary()
	binary.NativeEndian.PutUint32(want[4:], uint32(len(want))|imsgFDMark)
	if !bytes.Equal(w.b.Bytes(), want) {
		t.Fatalf("Encode() got %x, want %x", w.b.Bytes(), want)
	}
	fds, err := parseRights(w.oobs[0])
	if err != nil || len(fds) != 1 || fds[0] != int(file.Fd()) {
		t.Fatalf("first write got descriptors %v, error %v", fds, err)
	}
	for _, oob := range w.oobs[1:] {
		if len(oob) != 0 {
			t.Fatal("descriptor sent again after a partial write")
		}
	}
	checkFile(t, file)
}

func TestUnixEncodeErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	for _, name := range []string{"write error", "short control", "no progress"} {
		t.Run(name, func(t *testing.T) {
			w := &chunkUnixWriter{chunk: 3}
			wantErr := writeErr
			switch name {
			case "write error":
				w.failAt, w.err = 2, writeErr
			case "short control":
				w.shortOOB, wantErr = true, ErrFD
			case "no progress":
				w.noProgress, wantErr = true, io.ErrShortWrite
			}
			file := testFile(t)
			enc := &UnixEncoder{conn: w, bo: binary.NativeEndian}
			err := enc.Encode(&IMsg{Data: []byte("payload")}, file)
			if !errors.Is(err, wantErr) {
				t.Fatalf("Encode() got %v, want %v", err, wantErr)
			}
			calls := len(w.oobs)
			if again := enc.Encode(&IMsg{}, file); again != err || len(w.oobs) != calls {
				t.Fatal("failed encoder wrote again or changed its error")
			}
			checkFile(t, file)
		})
	}
}

func TestUnixArguments(t *testing.T) {
	if _, err := NewUnixEncoder(nil); err == nil {
		t.Fatal("nil socket accepted")
	}
	if _, err := NewUnixDecoder(nil); err == nil {
		t.Fatal("nil socket accepted")
	}
	left, right := unixPair(t)
	if _, err := NewUnixEncoderWithByteOrder(left, nil); err == nil {
		t.Fatal("nil byte order accepted")
	}
	if _, err := NewUnixDecoderWithByteOrder(right, nil); err == nil {
		t.Fatal("nil byte order accepted")
	}
	w := &chunkUnixWriter{chunk: MaxSizeInBytes}
	enc := &UnixEncoder{conn: w, bo: binary.NativeEndian}
	file := testFile(t)
	if err := enc.Encode(nil, nil); err != nil || len(w.oobs) != 0 {
		t.Fatal("nil message and file were not a NOOP")
	}
	if err := enc.Encode(nil, file); err == nil {
		t.Fatal("file without message accepted")
	}
	if err := enc.Encode(&IMsg{Data: make([]byte, MaxDataSizeInBytes+1)}, nil); err != ErrLength {
		t.Fatalf("oversized message got %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(&IMsg{}, file); err == nil {
		t.Fatalf("closed file got %v", err)
	}
	if len(w.oobs) != 0 {
		t.Fatal("argument error wrote to socket")
	}
	if err := enc.Encode(&IMsg{}, nil); err != nil {
		t.Fatalf("argument errors poisoned encoder: %v", err)
	}
	r := &scriptedUnixReader{steps: []unixReadStep{{b: w.b.Bytes()}}}
	dec := &UnixDecoder{conn: r, bo: binary.NativeEndian}
	if _, err := dec.Decode(nil); err == nil || r.calls != 0 {
		t.Fatal("nil destination accepted or read from socket")
	}
	if received, err := dec.Decode(&IMsg{}); err != nil || received != nil {
		t.Fatalf("argument error poisoned decoder: file %v, error %v", received, err)
	}
}

func TestUnixConcurrentEncodeDecode(t *testing.T) {
	left, right := unixPair(t)
	enc, err := NewUnixEncoder(left)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewUnixDecoder(right)
	if err != nil {
		t.Fatal(err)
	}
	file := testFile(t)
	const count = 16
	var sends sync.WaitGroup
	errs := make(chan error, count*2)
	for i := range count {
		sends.Go(func() {
			im := IMsg{Type: uint32(i), Data: []byte(fmt.Sprint(i))}
			var attached *os.File
			if i%2 == 1 {
				attached = file
			}
			errs <- enc.Encode(&im, attached)
		})
	}
	var reads sync.WaitGroup
	seen := make(chan uint32, count)
	for range count {
		reads.Go(func() {
			var got IMsg
			received, err := dec.Decode(&got)
			if err == nil && (string(got.Data) != fmt.Sprint(got.Type) || (received != nil) != (got.Type%2 == 1)) {
				err = fmt.Errorf("message and descriptor interleaved: %#v", got)
			}
			if received != nil {
				_ = received.Close()
			}
			errs <- err
			seen <- got.Type
		})
	}
	sends.Wait()
	reads.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	close(seen)
	unique := make(map[uint32]bool)
	for typ := range seen {
		if unique[typ] {
			t.Fatalf("duplicate message %d", typ)
		}
		unique[typ] = true
	}
}
