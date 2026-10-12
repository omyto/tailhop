# tailhop

> A tiny forward proxy that lets devices on your Tailnet access the internet through a remote host, powered by tsnet.

tailhop is a Go HTTP/1.1 forward proxy enrolled as its own Tailscale node. It accepts connections only through tsnet and connects to public destinations through the container's ordinary network stack. Plain HTTP requests stream through a standard HTTP transport; CONNECT tunnels HTTPS or other TCP streams without TLS interception. Ports 80 and 443 are the only permitted destination ports.

```text
Tailnet client -> tsnet.Listen -> HTTP proxy -> net.Dialer -> container default route
```

The MVP includes destination validation, hop-by-hop/proxy credential header removal, streaming uploads and responses, tracked CONNECT tunnels, bounded shutdown, and credential-free tests. [SPEC.md](SPEC.md) remains the implementation contract.

## Docker quick start

Install Docker with Compose and provide the container outbound connectivity for Tailscale and internet destinations.

```sh
cp .env.example .env
chmod 600 .env
# Edit .env: replace TS_AUTHKEY with an enrollment key from your Tailnet.
docker compose config --quiet
docker compose up --build -d
docker compose logs -f tailhop
```

Configure Tailnet grants/ACLs to allow only intended clients to reach this node on TCP 8080. Confirm the node appears connected in the Tailscale device view. Enrollment keys must be appropriate for your Tailnet's approval/tag policy. After enrollment, remove TS_AUTHKEY from .env and recreate the container:

```sh
docker compose up -d --force-recreate
```

The named state volume preserves node identity. Do not use `docker compose down -v` unless you intend to delete that identity. Without an auth key, an uninitialized tsnet node may log an interactive enrollment URL. Treat that URL and persisted state as secrets. Automated deployments should supply a key.

The image runs as UID/GID **10001:10001** from its first application start. Its state directory is pre-created with ownership 10001:10001 and mode 0700; Docker copies those permissions into a new, empty named volume. Keep Docker's default volume copy-up enabled. Existing volumes must already have that ownership. For a host bind mount, prepare it explicitly before running:

```sh
sudo install -d -m 0700 -o 10001 -g 10001 ./state
```

The application verifies state writability and fails early on incorrect permissions. No TUN device, NET_ADMIN, privileged mode, host networking, or published port is used. Compose drops all capabilities and sets no-new-privileges. SIGTERM/SIGINT stop HTTP acceptance and allow requests and tunnels to drain for up to ten seconds, then force-close remaining connections; Compose allows twenty seconds for proxy drain and tsnet cleanup before forced termination.

## Verify from a Tailnet client

Use MagicDNS, the actual node DNS name, or its Tailscale IP. Force curl to use the proxy even if the client has NO_PROXY configured:

```sh
curl --noproxy '' --proxy http://tailhop:8080 http://example.com/
curl --noproxy '' --proxy http://tailhop:8080 https://ifconfig.me/ip

# Expected: HTTP 403
curl --noproxy '' -i --proxy http://tailhop:8080 http://127.0.0.1/
curl --noproxy '' -i --proxy http://tailhop:8080 http://169.254.169.254/
curl --noproxy '' -i --proxy http://tailhop:8080 http://example.com:22/
```

The IP service should show the actual **container egress path**, usually Docker host/upstream NAT, which can differ with VPNs, routing policy, or cloud networking. Compare it with the client's direct public IP. Restart without an enrollment key and confirm the same node identity survives; run `docker compose down` and confirm clean termination.

## Configuration

Only environment variables are supported:

| Variable | Default | Purpose |
| --- | --- | --- |
| TS_AUTHKEY | unset | Initial enrollment; unnecessary with enrolled state |
| TS_HOSTNAME | tailhop | Node hostname (DNS label) |
| TS_STATE_DIR | /var/lib/tailhop | Persistent, private, writable state directory |
| PROXY_PORT | 8080 | Tailnet listener TCP port, 1–65535 |
| LOG_LEVEL | info | debug, info, warn, or error |
| MAX_CLIENTS | 100 | Positive integer; global simultaneous inbound TCP connection limit |
| IDLE_TIMEOUT | 10m | Positive Go duration; HTTP keep-alive and CONNECT inactivity |

Do not commit .env or state. Application logs include the source IP from the tsnet connection, methods, validated hostnames, status and duration, never request bodies, URL queries or proxy credentials. Forwarding headers are ignored for logging identity; no per-request WhoIs lookup is used. Verbose tsnet backend output is suppressed; tsnet user status/enrollment messages use structured logging. Tailscale's library also manages its own operational log uploads.

## Connection lifecycle and inactivity

The default resource model is inspired by Tinyproxy's MaxClients 100 and Timeout 600, with two environment settings rather than quotas or a worker pool. MAX_CLIENTS counts live **inbound TCP connections**, globally across all source IPs. A connection holds one slot from admission until full close: idle HTTP keep-alive, active HTTP requests, DNS/dialing CONNECT requests, and hijacked tunnels all count. Reusing keep-alive does not acquire another slot; TCP half-close does not release it while the other direction remains usable.

At capacity, the listener accepts and immediately closes excess sockets, without an HTTP response, queue, or rejection goroutine. Clients may see EOF/reset or a proxy connection error. This also applies before request headers arrive. A retry can succeed after capacity becomes available. Failed requests may retain their slot if the client keeps the HTTP connection alive; closing it, expiration, or shutdown releases the slot exactly once.

IDLE_TIMEOUT defaults to ten minutes. The HTTP server closes idle keep-alive connections after that interval; the existing ten-second header timeout applies when parsing request headers. CONNECT uses one shared inactivity timer, reset by successful forwarded writes in **either** direction. A continuous upload or download keeps the tunnel alive. When no bytes can be delivered for the interval, both peers are closed, including a half-closed or stalled tunnel. No per-connection monitoring goroutine or absolute tunnel lifetime is used.

Unlike Tinyproxy's broader connection inactivity semantics, IDLE_TIMEOUT does **not** time out active ordinary HTTP request/response bodies. Streaming/SSE and full-duplex uploads retain no blanket read/write deadline, and response writes continue to flush immediately. Stalled HTTP bodies can therefore hold a slot until disconnect or shutdown. DNS/dialing remains bounded by ten seconds and upstream response headers by thirty seconds. Upstream pooled HTTP idle connections retain the separate sixty-second expiry.

Shutdown stops admission and cancels pending CONNECT DNS/dial operations immediately, preventing late tunnel registration. Existing HTTP streams and CONNECT tunnels share a ten-second graceful drain budget. Pending HTTP dials also respect client cancellation, even when the standard transport detaches its dial context for pooling. At expiry, outbound HTTP operations are canceled and all remaining inbound/tunnel connections are force-closed. Handler completion is observed during the budget; cleanup already in progress after forced close is not waited on beyond the deadline.

One client can occupy **all** slots. This is a global connection bound, not per-client fairness, bandwidth control, or a complete memory/CPU limit. Restrict Tailnet grants/ACLs and apply container CPU/memory/process limits appropriate to the workload (for example, through a Compose override). Resource caps may require tuning for tsnet and expected traffic.

## Security and limitations

Tailnet access controls are the inbound authorization boundary; there is no additional user/password authentication or per-client quota. Restrict access to trusted clients. Like any forward proxy, this service can consume bandwidth and connections, so apply deployment resource limits and egress firewall rules appropriate to your environment.

Every new upstream TCP connection resolves under a ten-second DNS/dial budget, filters the answers, and dials a vetted **numeric IP**. HTTP connection reuse stays pinned to the already vetted peer. Mixed DNS answers use only allowed, deduplicated candidates. IPv4 and IPv6 are interleaved while preserving resolver order within each family. Attempts are sequential, with no racing dial goroutines. Non-final candidates receive at most two seconds and at most their fair share of the remaining budget; the final candidate receives the remaining time. DNS resolution and all attempts share one ten-second deadline. A slow address may be abandoned to permit fallback. No environment HTTP_PROXY/HTTPS_PROXY is used for proxied egress.

The policy unmaps IPv4-mapped IPv6 before classification and blocks loopback, private, link-local, CGNAT, metadata, multicast, documentation, benchmarking and reserved space. It conservatively excludes IPv6 transition space and accepts IPv6 only within 2000::/3 minus special-purpose exclusions. Special-use public exceptions within excluded prefixes are deliberately denied. Prefix decisions are based on the [IANA IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) and [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) registries; review them when upgrading. IP classification cannot compensate for an environment that routes nominally public addresses to sensitive internal services.

Only absolute-form http:// requests are accepted; use CONNECT for HTTPS. Redirects are returned to the client. Host is normalized from the target URL. Hop-by-hop headers and trailers are stripped; trailers are not forwarded. Active HTTP bodies have no blanket timeout; CONNECT has the shared inactivity timeout described above. Automatic upstream decompression is disabled to preserve content encoding. Upstream response headers have a 32 KiB bound and a thirty-second timeout, client headers ten seconds and a 32 KiB bound; upstream idle connections are bounded and expire after sixty seconds.

Plain HTTP remains unencrypted on the internet side. CONNECT preserves the client's end-to-end TLS without inspecting it. TCP protocols on 80/443 can be tunneled; this is not an HTTP-only content filter. No SOCKS, HTTP/2 CONNECT, Upgrade/WebSockets, reverse proxy, dashboard, private-subnet access, caching, or whole-device exit-node routing is implemented.

## Development

The module requires Go 1.27.1 (matching the mise toolchain and Tailscale requirement); the Docker builder uses Go 1.27.2. tailscale.com v1.104.1 is pinned. Tests use only injected resolvers/dialers and test-local connections; no Tailnet credentials or internet are needed after dependency download.

```sh
mise exec -- gofmt -w ./cmd ./internal
mise exec -- go vet ./...
mise exec -- go test ./...
mise exec -- go test -race ./...
docker compose config --quiet
docker build .
```

Race instrumentation requires a C compiler and CGO enabled.

Docker image construction and first-run volume permissions must be checked on a host with daemon access. Real enrollment, Tailnet ACL enforcement, observed egress IP, persisted identity and signal shutdown require the manual smoke tests above.
