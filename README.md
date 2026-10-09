# tailhop

> A tiny forward proxy that lets devices on your Tailnet access the internet through a remote host, powered by tsnet.

**Status:** specification only; implementation is not yet included.

## What it does

tailhop is a small Go HTTP forward proxy that runs as its own [Tailscale](https://tailscale.com/) node using [`tsnet`](https://pkg.go.dev/tailscale.com/tsnet). Tailnet clients explicitly configure tailhop as their HTTP/HTTPS proxy. tailhop accepts these connections through the Tailnet and sends outbound traffic through the container's normal network stack, typically NATed through its Docker host.

```text
Client on Tailnet
    |
    | HTTP proxy request / HTTPS CONNECT
    v
Tailnet --> tsnet listener (:8080)
                |
                v
           Go proxy handler
                |
                | regular net.Dialer / HTTP transport
                v
        Container default route
                |
                v
       Docker host / upstream NAT
                |
                v
           Public Internet
```

It is **not** a Tailscale Exit Node: only applications configured to use this forward proxy send traffic through it. It does not transparently route a client's entire network connection, and it does not serve as a reverse proxy.

## Planned MVP

- HTTP/1.1 forward proxy for plain `http://` requests.
- HTTP `CONNECT` tunneling, including HTTPS (without inspecting TLS).
- Inbound listening **only** through `tsnet.Server.Listen` (no host-published port).
- Outbound connections through ordinary Go networking (never `tsnet.Dial`).
- Default-deny destination rules for non-public IP addresses; destination TCP ports limited to 80 and 443 in v1.
- Environment-based configuration, graceful shutdown, unit tests, and a non-root Docker image.

No SOCKS5, dashboard, authentication database, caching, HTTPS interception, or transparent routing in the MVP.

See [SPEC.md](SPEC.md) for the authoritative behavior and security requirements.

## Intended usage (after implementation)

The implementation will include `Dockerfile` and `compose.yaml`. First start should use a Tailscale auth key, and the `tsnet` state directory should be persisted so that restarts reuse the node identity.

From another device on the same Tailnet (with appropriate Tailnet access controls):

```sh
curl --proxy http://tailhop:8080 http://example.com/
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

The hostname `tailhop` assumes that name resolves for your Tailnet (for example, via MagicDNS); otherwise, use the node's Tailscale IP or its actual Tailnet DNS name. A successful public-IP test should report the outbound public IP of the container's network path, **usually** the Docker host's public egress IP—not necessarily its literal interface IP on every network setup.

## Security model

- A Tailnet is the **transport and inbound access boundary**, not unrestricted authorization to access destinations.
- Configure Tailscale **grants/ACLs** so only trusted clients can reach the tailhop node's proxy TCP port. A permissive Tailnet policy may allow more clients than you expect.
- The proxy must independently deny loopback, private, link-local, CGNAT, metadata, and other non-public destinations; resolve and validate before each new upstream connection and dial the validated IP directly to prevent DNS rebinding TOCTOU bypasses.
- Only port 80 or 443 is allowed in the MVP, whether requested with a plain HTTP URL or CONNECT.
- Treat the Tailscale auth key and persisted state as secrets; do not commit `.env` or state files.
- Plain HTTP requests are not end-to-end encrypted beyond the Tailnet link. For HTTPS, CONNECT tunnels TLS end-to-end from the client to the origin (the proxy does not decrypt it).

## Development

After the implementation is generated:

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go test -race ./...
```

`go test -race` may be limited by the build environment; its result should be reported separately. Unit tests should not require actual Tailscale credentials or internet access.

## Reference documentation

- [Tailscale: tsnet basics](https://tailscale.com/docs/features/tsnet/how-to/create-basic-tsnet-app)
- [Tailscale: tsnet.Server API](https://tailscale.com/docs/reference/tsnet-server-api)
- [Go: net/http](https://pkg.go.dev/net/http)
- [Go: net/netip](https://pkg.go.dev/net/netip)
