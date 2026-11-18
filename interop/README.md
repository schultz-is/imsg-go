# tmux interoperability tests

This is a separate Go module so the tests can live in the repository without being
included in the library's module ZIP or normal test suite. It exercises the library
against an installed tmux binary; it does not compile C fixtures.

Supported tmux versions are 3.6, 3.6a, 3.7, and 3.8. Earlier versions use a different
imsg header. Missing or unsupported tmux versions fail these tests rather than
silently skipping them.

## Running locally

Run the tests directly from this directory:

```sh
go test -v ./...
```

The workspace in `interop/go.work` automatically uses the parent library checkout
instead of the published version required by `go.mod`. It applies only when running
from this directory; the root library's module and test suite remain independent.
No `replace` directive is needed. If `GOWORK` is explicitly set to `off` or another
workspace, unset it to use this workspace.

To collect coverage for the library exercised by these tests, select it explicitly:

```sh
go test -coverpkg=github.com/schultz-is/imsg-go -coverprofile=/tmp/coverage.out ./...
```

Without `-coverpkg`, Go measures only the interoperability module, which contains
tests rather than library implementation code.

Set `IMSG_TMUX_DEBUG=1` to enable tmux logging. On failure, the tests include the
tails of those logs in their output.

## Coverage and isolation

- A real tmux client connects to a Go listener. The test receives its stdin/stdout
  descriptors and verifies that they refer to the expected files.
- A Go client sends stdin/stdout descriptors to a private tmux server using the
  library's imsg transport. After the sender closes its copies, the server reads a
  command and writes a response through the passed descriptors.
- Servers run in the foreground with `/dev/null` as their configuration file.
  Sockets and logs use test-owned temporary directories. Cleanup signals only the
  processes started by the test; it never uses the default tmux socket.

CI runs the library tests and harness together on macOS and Ubuntu 26.04, installing
prebuilt tmux packages.
These checks exercise portable imsg interoperability, not native OpenBSD execution.
