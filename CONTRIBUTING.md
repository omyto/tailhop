# Contributing to tailhop

Focused fixes, tests, and documentation improvements are welcome. Keep tailhop a
tiny, explicit Tailnet-native forward proxy: standard Go networking, tsnet inbound,
ordinary host networking outbound, and small, auditable changes. Read [SPEC.md](SPEC.md)
for the implementation contract and scope before proposing behavioral changes.

## Build and test

Install Go 1.27.3 or newer and run these commands from the repository root.
`go.mod` enforces 1.27.3 as the minimum security-patched toolchain; official CI,
Linux release builds, and the Docker builder resolve to Go 1.27.3. A separate
`toolchain` directive is unnecessary. Dependencies remain pinned in `go.mod` and
`go.sum`. A version manager such as mise is optional.

```sh
go build ./cmd/tailhop
gofmt -l cmd internal
go vet ./...
go test ./...
go test -race ./...
```

For the Docker development workflow, build and run from the checkout:

```sh
docker compose -f compose.yaml up --build -d
docker compose -f compose.yaml logs -f tailhop
```

No `.env` or auth key is required; complete browser enrollment when needed.
See the [Docker development guide](docs/docker.md#development-build-from-source).
End users should use the official prebuilt image rather than this build path.

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

## Third-party licenses

When updating dependencies or bundled assets, review their redistribution terms
and update [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES) as needed.
