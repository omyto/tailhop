# tailhop — MVP specification

**Status:** implementation contract for Codex. If the README and this document disagree, follow this document and update the README accordingly.

## 1. Goal and traffic direction

Create a **small, explicit HTTP forward proxy** written in Go. The proxy is an independently enrolled Tailscale node running *inside the Go process* through `tailscale.com/tsnet`. Tailnet peers connect **inbound** through `tsnet.Server.Listen("tcp", ":<PROXY_PORT>")`. The proxy connects **outbound** using Go's normal network stack, not `tsnet.Dial` and not a Tailscale Exit Node.

```text
Tailnet client --> Tailscale --> tsnet.Listen --> forward proxy
                                               |
                                     normal net.Dialer
                                               |
                                  container default route
                                               |
                                        public internet
```

The container needs outbound connectivity for both Tailscale control/peer traffic and proxied destinations. With ordinary Docker bridge networking, outbound traffic typically exits through the Docker host's NAT; do not require `network_mode: host` or assume every deployment uses the same egress IP.

## 2. Scope

### Required

1. HTTP/1.1 **forward proxy** for absolute-form `http://host[:port]/path` requests. Preserve HTTP methods, streaming request bodies, streaming response bodies, status, and end-to-end headers. Do not implement a reverse proxy.
2. HTTP/1.1 **CONNECT** for tunneling arbitrary TCP streams to an authorized `host:port`; HTTPS works without TLS interception. Establish the upstream connection before returning `200 Connection Established`.
3. Concurrent client requests and concurrent tunnels.
4. `tsnet.Server` as the **only** incoming listener; do not call `net.Listen`, start a diagnostic/admin HTTP port, use `ListenFunnel`, or publish Docker ports.
5. Ordinary `net.Dialer`-based outbound connections, and a standard `http.Transport` for plain HTTP forwarding, with a centralized outbound destination policy applied to **every actual TCP dial**.
6. Persistent node identity and clean SIGINT/SIGTERM handling.
7. `log/slog` with structured, modest logging. No URL query strings, credentials, auth keys, or proxy header values in logs.
8. Docker deployment without root, TUN, `NET_ADMIN`, privileged mode, or host-published ports.

### Non-goals

SOCKS4/5, UDP/QUIC forwarding, TLS MITM, HTTPS proxy listener, HTTP/2 CONNECT, WebSockets/Upgrade forwarding, transparent interception, whole-device exit-node routing, reverse proxying, caching, DNS proxying, traffic analytics, dashboards, per-user auth, auto-updaters, metrics exporters, and support for private/intranet destination access. Do not add knobs or dependencies for these.

## 3. Client-facing HTTP behavior

### Ordinary HTTP requests

- Only accept proxy-form absolute URLs with scheme `http` and a valid hostname. Reject origin-form (`GET /...`) and unsupported URL schemes. Do not treat an incoming absolute `https://` URL as a reason to implement an additional TLS client mode; require CONNECT for HTTPS.
- Accept standard HTTP methods other than CONNECT; do not accidentally block `POST`, `PUT`, etc. Reject malformed target authorities, invalid port syntax, or userinfo embedded in the URL.
- Permit destination ports **80 and 443 only** in v1 (when no port is present, HTTP defaults to 80). This is a deliberate MVP restriction, not configurable in v1.
- Construct an outgoing origin request without retaining proxy-specific or hop-by-hop headers. Remove named headers listed in the `Connection` header as well as `Connection`, `Proxy-Connection`, `Proxy-Authorization`, `Proxy-Authenticate`, `Keep-Alive`, `TE`, `Trailer`, `Transfer-Encoding`, and `Upgrade` as appropriate for the outgoing request/response. Do not forward sensitive proxy credentials to an origin. Preserve legitimate end-to-end headers and request body streaming.
- Route with an `http.Transport` whose `Proxy` is explicitly `nil` (never inherit `HTTP_PROXY` or `HTTPS_PROXY` for upstream traffic), and whose `DialContext` uses the central validating dialer. Use the URL authority as the destination; avoid Host/header ambiguity by normalizing outgoing Host for this MVP.
- Do not automatically follow redirects: pass origin response and `Location` back to the client. Do not buffer complete bodies.
- Return appropriate errors (`400` malformed input, `403` blocked destination, `405` unsupported mode as appropriate, `502` upstream error, `504` timeout) without revealing internal implementation details.

### CONNECT

- Require a valid `host:port` authority with explicit port (IPv6 literal syntax must use brackets), with destination port 80 or 443 only.
- Resolve and validate the upstream destination **before** responding with status 200. Reject blocked destinations (`403`) or failed upstream connections (`502`/`504`).
- After a successful upstream dial, take ownership of the client connection using `http.Hijacker` and send an HTTP/1.1 `200 Connection Established` response.
- Tunnel raw bytes in both directions without inspecting TLS. Correctly forward any bytes already buffered by the hijacked reader, if applicable; don't lose early client data.
- Avoid goroutine leaks: manage the two copy directions and termination (including half-close where supported, with a safe fallback). Close both sides on terminal error, shutdown, or timeout; keep long-lived active tunnels working until termination instead of applying a blanket request-body timeout.
- The application **must track active hijacked tunnels**. `http.Server.Shutdown` does not own or wait for them. On SIGTERM/SIGINT, stop accepting new requests, give active tunnels a bounded drain period, then close remaining tunnels and finish shutdown within a finite deadline.

## 4. Destination policy (security-critical)

All outbound TCP dials for both CONNECT and ordinary HTTP **must** call a single, independently testable destination-validating dialer. The policy runs in the application, not just Tailscale access controls.

1. Parse and validate the requested authority and port (80/443 only). Explicitly handle IPv4 literals, IPv6 literals (including bracket rules), and DNS hostnames.
2. If the host is a literal IP, validate it directly. Otherwise call a resolver that can be replaced in tests, with a bounded timeout.
3. For each dial attempt, filter resolved IPs through the public-address policy; if no permitted address remains, fail closed. Dial a **validated numeric IP address** using `net.Dialer.DialContext`; never validate a hostname and subsequently pass that same hostname to a second resolver (DNS rebinding/TOCTOU).
4. Reject loopback, private, link-local, unspecified, multicast, CGNAT (`100.64.0.0/10`), broadcast, documentation, benchmarking, reserved/special-purpose, and other known non-public ranges for both IPv4 and IPv6, including IPv4-mapped IPv6 by normalizing with `Unmap`. Deny cloud instance metadata endpoints (including `169.254.169.254`) as a consequence of the IP policy. Fail closed for addresses that cannot be confidently classified.
5. **Do not rely on `netip.Addr.IsGlobalUnicast()` alone**: it reports true for some private/special-use addresses. Use explicit address-classification checks and documented reserved-prefix exclusions, covered by tests.
6. The policy must apply at **connection time**, including any newly opened pooled HTTP transport connection. No user-configurable policy bypass for the MVP.
7. If multiple IPs resolve, dial only allowed candidates. Timeouts/cancellations must be respected and connections closed on failure.

The scope is public-internet egress only. Do not introduce a private-subnet allowlist in v1.

## 5. Configuration

Use only environment variables. Validate configuration early; report useful errors without exposing secrets.

| Variable | Default | Meaning |
| --- | --- | --- |
| `TS_AUTHKEY` | unset | Auth key for initial node enrollment; `tsnet` may read it directly. Already-enrolled persistent state should work without it. |
| `TS_HOSTNAME` | `tailhop` | Tailscale node hostname, set through `tsnet.Server.Hostname`. |
| `TS_STATE_DIR` | `/var/lib/tailhop` | Directory passed to `tsnet.Server.Dir`; create if needed with secure permissions and ensure writable as container UID. Persist in Docker. |
| `PROXY_PORT` | `8080` | Tailnet-only listening TCP port, valid 1–65535. |
| `LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. |

Use `tsnet.Server.Listen("tcp", fmt.Sprintf(":%d", port))`; don't bind `0.0.0.0` on the Docker host. Do not require `TS_AUTHKEY` on every restart. For an uninitialized node without a key, document that `tsnet` may output an interactive enrollment URL; automated deployment should set an auth key. Do not set the tsnet node to ephemeral when relying on persisted identity.

## 6. Reliability, resource usage, and shutdown

- Use reasonable concrete defaults: `ReadHeaderTimeout` ~10s, a bounded header size (e.g. 32 KiB), DNS lookup/dial timeout ~10s, TLS/upstream timeout where applicable. Define these as internal constants unless configuration is necessary for correctness.
- Do not set `http.Server.ReadTimeout`/`WriteTimeout` globally in a way that breaks HTTP streaming or CONNECT tunnels. Do not buffer whole requests/responses.
- Bound idle upstream HTTP connection count and idle time. Close idle transport connections on shutdown.
- Start serving with `http.Server.Serve` on the `tsnet` listener. Close listener and server, drain then terminate tracked tunnels, and close `tsnet.Server` in a predictable order without hanging.
- Exclude stack traces, request bodies, auth keys, and full destination URLs from ordinary logs. Logging only status, method, duration and destination hostname (no query) is sufficient.
- Avoid clever abstractions and hidden goroutine lifetimes. The implementation should be easy to audit.

## 7. Repository layout (target, not a framework)

```text
cmd/tailhop/main.go
internal/proxy/proxy.go
internal/proxy/proxy_test.go
internal/proxy/policy.go
internal/proxy/policy_test.go
Dockerfile
compose.yaml
.dockerignore
.gitignore
go.mod
go.sum
README.md
SPEC.md
```

Add at most one small extra internal Go file if it materially improves clarity. Avoid `pkg/`, dependency injection frameworks, routers, CLI frameworks, heavy proxy libraries, and config frameworks. Prefer Go standard library plus `tailscale.com/tsnet` and its transitive dependencies. Let the current supported Go toolchain and stable compatible `tailscale.com` version determine `go.mod`; do not guess versions or pin an obsolete release.

## 8. Docker requirements

- Multi-stage Dockerfile: build Go binary in a Go image; run with minimal runtime containing suitable CA roots as required by tsnet networking.
- Run as **non-root**. State mount target `/var/lib/tailhop` must be writeable by the runtime UID. If using a named volume, include reliable first-run permissions handling without starting the app as root; explain host bind-mount ownership requirements.
- Compose example must use a persistent named volume for state, set `TS_STATE_DIR`, and pass `TS_AUTHKEY` through the environment (recommend `.env` local file; never hardcode a key).
- Do not include `ports:`, `network_mode: host`, `privileged: true`, `cap_add`, or `/dev/net/tun`.
- Support clean `docker compose down` / SIGTERM. Provide `.env.example` with placeholders (not real secrets) and `.dockerignore`.
- Document expected default Docker bridge NAT behavior and caveats about the observed public egress IP.

## 9. Tests and verification

All unit tests must run **without** Tailnet access, auth credentials, or public internet. Make the handler and dial policy testable with injected resolver/dial function or an equivalent small seam. If tests require loopback origins, use an explicitly **test-only** policy override or fake dialer; do not weaken production policy.

Minimum coverage:

- HTTP: `GET`, `POST` with body, status/body/header forwarding, chunked/streaming behavior, error responses, origin-form/invalid URI rejection, hop-by-hop and `Proxy-Authorization` stripping, no environment-based upstream proxy.
- CONNECT: handshake **after** successful dial, bidirectional bytes, early buffered bytes, remote/client EOF and half-close, failed dial, malformed authority, port denial, tunnel cleanup on shutdown.
- Policy: public IPv4/IPv6 allowed; blocked loopback (`127.0.0.1`, `::1`), RFC1918, IPv6 ULA, link-local, CGNAT `100.64.0.1`, metadata `169.254.169.254`, benchmarking/doc ranges, IPv4-mapped IPv6; mixed DNS answers; DNS failure; rebinding case where DNS would change between resolution and dial; timeout/cancellation.
- Shutdown: requests stop being accepted and active CONNECT tunnels are terminated within the configured shutdown budget.
- Parallel-safe concurrent requests/tunnels; no nondeterministic sleeps where synchronization can be used.

Run and report:

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go test -race ./...
```

If dependency downloading, Docker, race instrumentation, or another environment constraint prevents a check, report it transparently; do not claim it passed. No real Tailnet test is mandatory in automated unit tests.

### Manual acceptance smoke test

1. `docker compose up --build -d` starts a tailhop node on the Tailnet, without published ports or privileged permissions. Ensure node shows as connected in the Tailscale admin/device view.
2. Tailnet grants/ACLs allow only intended clients to reach tailhop TCP port `8080`.
3. From an authorized Tailnet client with a resolvable MagicDNS name (otherwise use Tailscale IP):
   ```sh
   curl --proxy http://tailhop:8080 http://example.com/
   curl --proxy http://tailhop:8080 https://ifconfig.me/ip
   ```
4. Verify the public-IP response matches the **actual container egress path** (usually host NAT), not the Tailnet client's egress IP.
5. Restart the container without providing a new auth key; confirm node identity survives because the state directory is persisted.
6. Confirm requests to non-public destinations and non-80/443 ports are rejected, and `docker compose down` terminates cleanly.

## 10. Completion conditions

- Implementation honors the exact inbound/outbound direction and does not accidentally use `tsnet.Dial` for public destinations.
- HTTP forwarding and CONNECT work per spec without buffering complete payloads.
- Outbound IP checks cannot be bypassed by DNS resolution changes before dialing.
- CONNECT tunnels do not leak after shutdown (including hijacked-connection tracking).
- Docker runs non-root, persists state and publishes no ports.
- Tests run without real Tailnet credentials; requested verification commands have reported results.
- README reflects actual code and has a reproducible quick start and known limitations.
