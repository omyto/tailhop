# Contributing to tailhop

Focused fixes, tests, and documentation improvements are welcome. Keep tailhop a
tiny, explicit Tailnet-native forward proxy: standard Go networking, tsnet inbound,
ordinary host networking outbound, and small, auditable changes. Read [SPEC.md](SPEC.md)
for the implementation contract and scope before proposing behavioral changes.

## Build and test

Run from the repository root with the Go toolchain managed by mise. The module
requires Go 1.27.1 and pins tailscale.com v1.104.1; the Docker builder uses Go 1.27.2.
See [go.mod](go.mod) and [Dockerfile](Dockerfile) for the current requirements.

```sh
mise exec -- go build -trimpath -o tailhop ./cmd/tailhop
mise exec -- gofmt -w ./cmd ./internal
mise exec -- go vet ./...
mise exec -- go test ./...
mise exec -- go test -race ./...
```

Race instrumentation requires CGO enabled and a C compiler. Tests use injected
resolvers/dialers and test-local connections: no Tailnet credentials or public
internet are required after dependencies have been downloaded. Keep test overrides
out of production policy.

## Pull requests

Keep each pull request focused. Explain the problem, the resulting behavior, and
which checks you ran, including any checks you could not run. Add regression tests
for behavioral fixes and update documentation when configuration or behavior changes.
Avoid new dependencies or abstractions unless they solve a concrete problem simply.

Take particular care with destination validation, numeric-IP dialing, DNS rebinding,
proxy headers, and CONNECT ownership. Changes involving buffered tunnel bytes,
half-close, cancellation, inactivity, or shutdown need lifecycle and concurrent/race
coverage. Preserve streaming and the tsnet inbound / ordinary net.Dialer outbound
architecture; do not weaken destination restrictions to make tests pass.

For documentation changes, check relative links, configuration defaults, and command
examples against the repository. Deployment changes also need the manual checks in
the [Docker guide](docs/docker.md#manual-verification) or
[systemd guide](docs/systemd.md#manual-service-verification). Unit tests do not prove
live enrollment, ACL enforcement, state permissions, or service sandbox compatibility.
