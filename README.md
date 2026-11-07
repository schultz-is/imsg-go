# imsg-go [![Tests](https://github.com/schultz-is/imsg-go/actions/workflows/linux.yml/badge.svg)](https://github.com/schultz-is/imsg-go/actions/workflows/linux.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/schultz-is/imsg-go.svg)](https://pkg.go.dev/github.com/schultz-is/imsg-go)

This package provides a method for interacting with OpenBSD's [imsg](https://man.openbsd.org/imsg_init)
IPC messaging. Communication via imsg commonly occurs between privilege separated processes over a
stream-oriented Unix domain socket. Examples of imsg usage can be found in several utilities
including OpenBSD's [vmm](https://man.openbsd.org/vmm)/[vmd](https://man.openbsd.org/vmd)/[vmctl](https://man.openbsd.org/vmctl),
[OpenBGPD](https://www.openbgpd.org/), and [tmux](https://github.com/tmux/tmux).

## Wire format

Messages use OpenBSD's current imsg header: four 32-bit fields for the type, total message length,
peer ID, and PID. The header occupies 16 bytes, and messages are limited to 16384 bytes including
the header. The default encoding uses native byte order; methods accepting a byte order can be
used for recordings from other systems.

The high bit of the length marks an accompanying file descriptor. Decoding a message with this
marker returns `ErrUnsupportedFD`. File descriptor passing is not supported.

The earlier header used a 16-bit length and separate 16-bit flags. Big endian peers and recordings
must use the current 32-bit length format. Messages without descriptors within the size limit have
the same encoding in little endian order under both formats.

## Stability
This package is pre-v1 and as such, currently provides no stability guarantees. Go 1.26 and above are supported.
