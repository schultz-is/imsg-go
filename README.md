# imsg-go [![Tests](https://github.com/schultz-is/imsg-go/actions/workflows/linux.yml/badge.svg)](https://github.com/schultz-is/imsg-go/actions/workflows/linux.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/schultz-is/imsg-go.svg)](https://pkg.go.dev/github.com/schultz-is/imsg-go) [![Go Report Card](https://goreportcard.com/badge/github.com/schultz-is/imsg-go)](https://goreportcard.com/report/github.com/schultz-is/imsg-go)

This package provides a method for interacting with OpenBSD's [imsg](https://man.openbsd.org/imsg_init)
IPC messaging. Communication via imsg commonly occurs between privilege separated processes over a
stream-oriented Unix domain socket. Examples of imsg usage can be found in several utilities
including OpenBSD's [vmm](https://man.openbsd.org/vmm)/[vmd](https://man.openbsd.org/vmd)/[vmctl](https://man.openbsd.org/vmctl),
[OpenBGPD](https://www.openbgpd.org/), and [tmux](https://github.com/tmux/tmux).

## Stability
This package is pre-v1 and as such, currently provides no stability guarantees. Go 1.24 and above are supported.
