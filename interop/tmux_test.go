// Copyright 2026 Matt Schultz. All rights reserved.
// Use of this source code is governed by an ISC license that can be found in the LICENSE file.

//go:build unix

package interop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	imsg "github.com/schultz-is/imsg-go"
)

// tmux 3.6 through 3.8 use protocol 8 and the current imsg header.
// Constants are from tmux's tmux-protocol.h and CLIENT_CONTROL from tmux.h.
const (
	protocolVersion = 8
	identifyTerm    = 101
	identifyTTY     = 102
	identifyStdin   = 104
	identifyDone    = 106
	identifyPID     = 107
	identifyCWD     = 108
	identifyStdout  = 110
	identifyFlags   = 111
	commandMessage  = 200
	exitMessage     = 203
	controlFlag     = 0x2000
)

func tmuxBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal("tmux is required for the interoperability module")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "-V").CombinedOutput()
	if err != nil {
		t.Fatalf("tmux -V: %v\n%s", err, output)
	}
	version := strings.TrimSpace(string(output))
	switch version {
	case "tmux 3.6", "tmux 3.6a", "tmux 3.7", "tmux 3.8":
	default:
		t.Fatalf("unsupported tmux version %q; use 3.6, 3.6a, 3.7, or 3.8", version)
	}
	t.Log(version)
	return path
}

func socketPath(t *testing.T) string {
	t.Helper()
	// Keep the name short enough for Darwin's sockaddr_un.
	dir, err := os.MkdirTemp("", "imsg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "socket")
}

func tmuxEnvironment() []string {
	var env []string
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "TMUX=") || strings.HasPrefix(value, "TMUX_PANE=") ||
			strings.HasPrefix(value, "SHELL=") || strings.HasPrefix(value, "TERM=") ||
			strings.HasPrefix(value, "ENV=") || strings.HasPrefix(value, "BASH_ENV=") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "TERM=xterm-256color", "SHELL=/bin/sh")
}

type process struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	stderr bytes.Buffer
}

func startProcess(t *testing.T, path string, stdin, stdout *os.File, args ...string) *process {
	t.Helper()
	if os.Getenv("IMSG_TMUX_DEBUG") == "1" {
		args = append([]string{"-vv"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	p := &process{cmd: exec.CommandContext(ctx, path, args...), done: make(chan struct{})}
	p.cmd.Dir = t.TempDir()
	p.cmd.Env = tmuxEnvironment()
	if stdin != nil {
		p.cmd.Stdin = stdin
	}
	if stdout != nil {
		p.cmd.Stdout = stdout
	}
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		defer cancel()
		defer func() {
			if !t.Failed() {
				return
			}
			logs, _ := filepath.Glob(filepath.Join(p.cmd.Dir, "tmux-*.log"))
			for _, path := range logs {
				data, _ := os.ReadFile(path)
				if len(data) > 16000 {
					data = data[len(data)-16000:]
				}
				t.Logf("%s:\n%s", filepath.Base(path), data)
			}
		}()
		select {
		case <-p.done:
			return
		default:
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			cancel()
			<-p.done
		}
	})
	return p
}

func (p *process) wait(t *testing.T) {
	t.Helper()
	<-p.done
	if p.err != nil {
		t.Fatalf("tmux process: %v\n%s", p.err, p.stderr.Bytes())
	}
}

func send(t *testing.T, enc *imsg.UnixEncoder, typ uint32, data []byte, file *os.File) {
	t.Helper()
	im := imsg.IMsg{Type: typ, PeerID: protocolVersion, PID: ^uint32(0), Data: data}
	if err := enc.Encode(&im, file); err != nil {
		t.Fatalf("send tmux message %d: %v", typ, err)
	}
}

func TestTmuxToGo(t *testing.T) {
	tmux := tmuxBinary(t)
	socket := socketPath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	if _, err := input.WriteAt([]byte("native client input"), 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	client := startProcess(t, tmux, input, output, "-N", "-S", socket, "-f", "/dev/null", "list-sessions")
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	dec, err := imsg.NewUnixDecoder(conn)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := imsg.NewUnixEncoder(conn)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[uint32]*os.File)
	for {
		var message imsg.IMsg
		file, err := dec.Decode(&message)
		if err != nil {
			t.Fatal(err)
		}
		if file != nil {
			t.Cleanup(func() { _ = file.Close() })
			if files[message.Type] != nil || (message.Type != identifyStdin && message.Type != identifyStdout) {
				t.Fatalf("unexpected descriptor on tmux message %d", message.Type)
			}
			files[message.Type] = file
		}
		if message.PeerID != protocolVersion {
			t.Fatalf("unexpected tmux protocol %d", message.PeerID)
		}
		if message.Type == commandMessage {
			break
		}
	}
	if files[identifyStdin] == nil || files[identifyStdout] == nil {
		t.Fatal("tmux did not send both stdin and stdout")
	}
	b := make([]byte, len("native client input"))
	if _, err := files[identifyStdin].ReadAt(b, 0); err != nil || string(b) != "native client input" {
		t.Fatalf("native stdin got %q, error %v", b, err)
	}
	if _, err := files[identifyStdout].WriteAt([]byte("received native stdout"), 0); err != nil {
		t.Fatal(err)
	}
	b = make([]byte, len("received native stdout"))
	if _, err := output.ReadAt(b, 0); err != nil || string(b) != "received native stdout" {
		t.Fatalf("native stdout got %q, error %v", b, err)
	}
	send(t, enc, exitMessage, make([]byte, 4), nil)
	client.wait(t)
}

func TestGoToTmux(t *testing.T) {
	tmux := tmuxBinary(t)
	socket := socketPath(t)
	server := startProcess(t, tmux, nil, nil, "-D", "-S", socket, "-f", "/dev/null")
	until := time.Now().Add(10 * time.Second)
	for {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-server.done:
			t.Fatalf("tmux server exited: %v\n%s", server.err, server.stderr.Bytes())
		default:
		}
		if time.Now().After(until) {
			t.Fatal("tmux server did not create its socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	setup := exec.CommandContext(ctx, tmux, "-N", "-S", socket, "new-session", "-d", "-s", "imsg", "exec cat")
	setup.Env = tmuxEnvironment()
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("create private tmux session: %v\n%s", err, output)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	enc, err := imsg.NewUnixEncoder(conn)
	if err != nil {
		t.Fatal(err)
	}
	input, writeInput, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = writeInput.Close() })
	readOutput, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOutput.Close(); _ = output.Close() })
	if err := readOutput.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	send(t, enc, identifyFlags, binary.NativeEndian.AppendUint64(nil, controlFlag), nil)
	send(t, enc, identifyTerm, []byte("xterm-256color\x00"), nil)
	send(t, enc, identifyTTY, []byte{0}, nil)
	send(t, enc, identifyCWD, append([]byte(filepath.Dir(socket)), 0), nil)
	send(t, enc, identifyPID, binary.NativeEndian.AppendUint32(nil, uint32(os.Getpid())), nil)
	send(t, enc, identifyStdin, nil, input)
	send(t, enc, identifyStdout, nil, output)
	send(t, enc, identifyDone, nil, nil)
	// The server must keep working after the sender closes its copies.
	_ = input.Close()
	_ = output.Close()
	args := []string{"attach-session", "-t", "imsg"}
	command := binary.NativeEndian.AppendUint32(nil, uint32(len(args)))
	for _, arg := range args {
		command = append(command, []byte(arg)...)
		command = append(command, 0)
	}
	send(t, enc, commandMessage, command, nil)
	const marker = "imsg-fd-roundtrip"
	if _, err := fmt.Fprintf(writeInput, "display-message -p %s\n", marker); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(readOutput)
	for scanner.Scan() {
		if scanner.Text() == marker {
			return
		}
	}
	t.Fatalf("tmux did not use the passed stdin/stdout descriptors: %v", scanner.Err())
}
