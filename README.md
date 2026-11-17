# imsg-go [![Tests](https://github.com/schultz-is/imsg-go/actions/workflows/linux.yml/badge.svg)](https://github.com/schultz-is/imsg-go/actions/workflows/linux.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/schultz-is/imsg-go.svg)](https://pkg.go.dev/github.com/schultz-is/imsg-go)

This package provides a method for interacting with OpenBSD's [imsg](https://man.openbsd.org/imsg_init)
IPC messaging. Communication via imsg commonly occurs between privilege separated processes over a
stream-oriented Unix domain socket. Examples of imsg usage can be found in several utilities
including OpenBSD's [vmm](https://man.openbsd.org/vmm)/[vmd](https://man.openbsd.org/vmd)/[vmctl](https://man.openbsd.org/vmctl),
[OpenBGPD](https://www.openbgpd.org/), and [tmux](https://github.com/tmux/tmux).

## Compatibility

This package uses OpenBSD's current imsg header format with a 32-bit length field.
The older format with separate 16-bit length and flags fields is not supported.

On Unix systems, `UnixEncoder` and `UnixDecoder` support passing one file descriptor
with each message over a connected Unix stream socket. Datagram and sequenced-packet
sockets are not supported by these APIs.

The existing binary marshaling and generic stream APIs remain data-only. Their
decoders return `ErrUnsupportedFD` for headers marked as carrying a descriptor.

## Passing files

Keep the message in an `IMsg` and pass an optional `*os.File` separately. A nil file
means no descriptor is attached. For a connected `*net.UnixConn` named `conn`:

```go
encoder, err := imsg.NewUnixEncoder(conn)
if err != nil {
	return err
}
err = encoder.Encode(&imsg.IMsg{Type: 1, Data: []byte("open file")}, file)
```

Sending borrows `file`; the encoder never closes it. The caller can close its copy
after sending without invalidating the receiver's descriptor. A send error does
not guarantee that the peer did not already receive the descriptor.

```go
decoder, err := imsg.NewUnixDecoder(conn)
if err != nil {
	return err
}
var message imsg.IMsg
received, err := decoder.Decode(&message)
if err != nil {
	return err
}
if received != nil {
	defer received.Close()
	// Use the received file, pipe, socket, or device.
}
```

The caller owns each received file and must close it. Failed decoding leaves the
message unchanged and closes any descriptors received for that failed message.
`ErrFD` identifies missing, unexpected, excessive, or truncated descriptor
attachments.

Use one encoder and one decoder per connection, without mixing in other socket
reads or writes. Calls on each codec are serialized; sending and receiving can
run concurrently. The caller owns the connection. After a transport or protocol
error, close the connection; the failed codec returns the same error on subsequent
calls rather than attempting to resume an incomplete message.

Socket deadlines apply to both codecs. Received files are marked close-on-exec
using Go's Unix socket handling: atomic on OpenBSD and Linux, with a non-atomic
fallback on macOS. The `WithByteOrder` constructors affect only the imsg header,
not the operating system's descriptor control data.

## Tests

The normal tests are pure Go and run on macOS and Linux:

```sh
CGO_ENABLED=0 go test ./...
```

Other Unix targets receive build checks; native OpenBSD runtime tests are not
currently part of CI. The [nested interoperability module](https://github.com/schultz-is/imsg-go/tree/main/interop)
tests against prebuilt tmux binaries in separate macOS/Linux CI jobs. It is excluded
from the library's Go module ZIP and does not require C fixtures or VMs.

## Stability
This package is pre-v1 and as such, currently provides no stability guarantees. Go 1.26 and above are supported.
