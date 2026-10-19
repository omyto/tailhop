# tailhop

> A tiny forward proxy that lets devices on your Tailnet access the internet through a remote host, powered by tsnet.

tailhop is a Go HTTP/1.1 forward proxy enrolled as its own Tailscale node. It accepts connections only through tsnet and connects to public destinations through the deployment's ordinary network stack. Plain HTTP requests stream through a standard HTTP transport; CONNECT tunnels HTTPS or other TCP streams without TLS interception. Ports 80 and 443 are the only permitted destination ports.

```text
Tailnet client -> tsnet.Listen -> HTTP proxy -> net.Dialer -> normal default route
```

The MVP includes destination validation, hop-by-hop/proxy credential header removal, streaming uploads and responses, tracked CONNECT tunnels, bounded shutdown, and credential-free tests. [SPEC.md](SPEC.md) remains the implementation contract.

Choose [Docker / Compose](#docker-quick-start) or a [native Linux systemd service](#native-linux-with-systemd). Both run the same executable and independently enrolled tsnet node.

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

## Native Linux with systemd

Run these commands manually as an administrator on a systemd-based Linux host with CA certificates installed, from the repository root. Build as your regular user using mise:

```sh
mise exec -- go build -trimpath -o tailhop ./cmd/tailhop
```

Create the dedicated system group and non-login user if absent. If either already exists, confirm it is a suitable dedicated service account before continuing; do not repurpose an unrelated account. These commands use the distribution's `nologin` executable:

```sh
getent group tailhop >/dev/null || sudo groupadd --system tailhop
getent passwd tailhop >/dev/null || sudo useradd --system --gid tailhop --home-dir /var/lib/tailhop --no-create-home --shell "$(command -v nologin)" tailhop
```

Install the binary, root-only configuration, and unit. On an existing installation, keep the configured environment file instead of overwriting it with the example:

```sh
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo install -d -o root -g root -m 0700 /etc/tailhop
sudo install -o root -g root -m 0600 packaging/systemd/tailhop.env.example /etc/tailhop/tailhop.env
sudoedit /etc/tailhop/tailhop.env
sudo install -o root -g root -m 0644 packaging/systemd/tailhop.service /etc/systemd/system/tailhop.service
sudo systemctl daemon-reload
sudo systemctl enable --now tailhop
```

Before starting, replace the TS_AUTHKEY placeholder with an appropriate enrollment key, or remove its assignment if reusing valid enrolled state. The [environment example](packaging/systemd/tailhop.env.example) uses systemd `EnvironmentFile` syntax: `KEY=value`, without `export` or shell/Compose variable expansion. The [configuration table](#configuration) applies to both deployments. Keep TS_STATE_DIR at **/var/lib/tailhop**: the unit sets it, and it must not be overridden in the environment file.

systemd reads the root-owned 0600 environment file before switching to `User=tailhop` / `Group=tailhop`. Environment variables are not a secret store: root and other processes running as the service account can inspect its environment, and diagnostic tools may expose it. Keep the account dedicated, avoid dumping the environment, and remove TS_AUTHKEY with `sudoedit` after successful enrollment. Restart the service to remove the key from the running process's environment. Valid persisted state reuses the same node identity without a new key; protect any interactive enrollment URL in the journal as a secret too.

`StateDirectory=tailhop` creates and owns /var/lib/tailhop for the service, with mode 0700. It persists across stops, restarts, and binary updates. Do not put state in /tmp. The unit runs without root, TUN, capabilities, a separate tailscaled daemon, firewall changes, or a public proxy listener. NoNewPrivileges, a private umask, read-only system paths and inaccessible home directories restrict writes; a private writable temporary directory supports temporary files while persistent state remains writable through StateDirectory.

The service uses Type=simple: active status means the process started, not that Tailnet enrollment is complete. Check the Tailscale device view and use the [client verification](#verify-from-a-tailnet-client) below. It starts after network-online.target, retries unexpected failures after five seconds, and receives SIGTERM on stop. TimeoutStopSec=20s allows the existing ten-second proxy drain plus tsnet cleanup before systemd force-kills a stuck process. Normal stdout/stderr go to journald.

Operations:

```sh
systemctl status tailhop
sudo journalctl -u tailhop -f
sudo systemctl restart tailhop
sudo systemctl stop tailhop
```

For an update, build the desired checkout, stop the service, replace the executable, and restart. Keep /etc/tailhop/tailhop.env and /var/lib/tailhop intact:

```sh
mise exec -- go build -trimpath -o tailhop ./cmd/tailhop
sudo systemctl stop tailhop
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo systemctl restart tailhop
systemctl status tailhop
```

Confirm the same device identity remains in the Tailscale device view and repeat the client Curl test. If updating the unit itself, install its new version and run `sudo systemctl daemon-reload` before restarting.

To uninstall the executable and unit:

```sh
sudo systemctl disable --now tailhop
sudo rm -f /etc/systemd/system/tailhop.service /usr/local/bin/tailhop
sudo systemctl daemon-reload
```

These commands preserve /var/lib/tailhop, /etc/tailhop, and the service account. Removing persisted tsnet state may require enrollment again and create a new node identity. Native service packaging here supports Linux with systemd.

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

Both deployments receive inbound proxy traffic only through tsnet; no public proxy port needs to be exposed. Restrict Tailscale grants/ACLs to intended peers on TCP 8080. For a simple HTTPS egress check:

```sh
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

The IP service shows the actual egress path: native systemd deployment uses the host's normal network route; Docker uses the container's route, usually through Docker host/upstream NAT. VPNs, routing policy, and cloud networking may change the observed address. Compare it with the client's direct public IP. Restart without an enrollment key and confirm the same node identity survives. Use `docker compose down` or `sudo systemctl stop tailhop` for the chosen deployment and confirm clean termination.

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
| IDLE_TIMEOUT | 10m | Positive Go duration; HTTP keep-alive, active HTTP forwarding, and CONNECT inactivity |

Do not commit .env or state. Application logs include the source IP from the tsnet connection, methods, validated hostnames, status and duration, never request bodies, URL queries or proxy credentials. Forwarding headers are ignored for logging identity; no per-request WhoIs lookup is used. Verbose tsnet backend output is suppressed; tsnet user status/enrollment messages use structured logging. Tailscale's library also manages its own operational log uploads.

## Connection lifecycle and inactivity

The default resource model is inspired by Tinyproxy's MaxClients 100 and Timeout 600, with two environment settings rather than quotas or a worker pool. MAX_CLIENTS counts live **inbound TCP connections**, globally across all source IPs. A connection holds one slot from admission until full close: idle HTTP keep-alive, active HTTP requests, DNS/dialing CONNECT requests, and hijacked tunnels all count. Reusing keep-alive does not acquire another slot; TCP half-close does not release it while the other direction remains usable.

At capacity, the listener accepts and immediately closes excess sockets, without an HTTP response, queue, or rejection goroutine. Clients may see EOF/reset or a proxy connection error. This also applies before request headers arrive. A retry can succeed after capacity becomes available. Failed requests may retain their slot if the client keeps the HTTP connection alive; closing it, expiration, or shutdown releases the slot exactly once.

IDLE_TIMEOUT defaults to ten minutes and covers HTTP keep-alive, active HTTP forwarding, and CONNECT. Ordinary HTTP connections use one shared inactivity timer: successful reads or writes on the inbound socket reset it. Stalled request bodies and response streams are closed when neither direction transfers data for the interval. Full close cancels blocked forwarding operations and releases the connection slot exactly once. Clients may see EOF, a reset, or a truncated response rather than an HTTP timeout response.

Continuous one-way uploads/downloads and streaming/SSE remain connected while data transfers continue; silence in the opposite direction does not expire them. There is no absolute body/connection lifetime or monitoring goroutine per connection. Timers recheck the last activity under a mutex so stale callbacks cannot close an active or reused connection. The HTTP server's keep-alive timeout remains in place, and its ten-second header deadline still applies even if partial header traffic resets inactivity.

CONNECT disables the HTTP activity timer before its bounded DNS/dial operation and retains its existing shared tunnel timer after hijacking. Successful forwarded writes to either peer reset tunnel inactivity; an idle or stalled tunnel closes both peers. TCP half-close and buffered data are preserved. Rejected CONNECT requests returning to HTTP keep-alive resume HTTP activity tracking. DNS/dialing remains bounded by ten seconds, upstream response headers by thirty seconds, and pooled upstream HTTP idle connections retain their separate sixty-second expiry.

Shutdown stops admission and cancels pending CONNECT DNS/dial operations immediately, preventing late tunnel registration. Existing HTTP streams and CONNECT tunnels share a ten-second graceful drain budget. Pending HTTP dials also respect client cancellation, even when the standard transport detaches its dial context for pooling. At expiry, outbound HTTP operations are canceled and all remaining inbound/tunnel connections are force-closed. Handler completion is observed during the budget; cleanup already in progress after forced close is not waited on beyond the deadline.

One client can occupy **all** slots. This is a global connection bound, not per-client fairness, bandwidth control, or a complete memory/CPU limit. Restrict Tailnet grants/ACLs and apply CPU/memory/process limits appropriate to the workload (for example, through a Compose override or a systemd drop-in). Resource caps may require tuning for tsnet and expected traffic.

## Security and limitations

Tailnet access controls are the inbound authorization boundary; there is no additional user/password authentication or per-client quota. Restrict access to trusted clients. Like any forward proxy, this service can consume bandwidth and connections, so apply deployment resource limits and egress firewall rules appropriate to your environment.

Every new upstream TCP connection resolves under a ten-second DNS/dial budget, filters the answers, and dials a vetted **numeric IP**. HTTP connection reuse stays pinned to the already vetted peer. Mixed DNS answers use only allowed, deduplicated candidates. IPv4 and IPv6 are interleaved while preserving resolver order within each family. Attempts are sequential, with no racing dial goroutines. Non-final candidates receive at most two seconds and at most their fair share of the remaining budget; the final candidate receives the remaining time. DNS resolution and all attempts share one ten-second deadline. A slow address may be abandoned to permit fallback. No environment HTTP_PROXY/HTTPS_PROXY is used for proxied egress.

The policy unmaps IPv4-mapped IPv6 before classification and blocks loopback, private, link-local, CGNAT, metadata, multicast, documentation, benchmarking and reserved space. It conservatively excludes IPv6 transition space and accepts IPv6 only within 2000::/3 minus special-purpose exclusions. Special-use public exceptions within excluded prefixes are deliberately denied. Prefix decisions are based on the [IANA IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) and [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) registries; review them when upgrading. IP classification cannot compensate for an environment that routes nominally public addresses to sensitive internal services.

Only absolute-form http:// requests are accepted; use CONNECT for HTTPS. Redirects are returned to the client. Host is normalized from the target URL. Hop-by-hop headers and trailers are stripped; trailers are not forwarded. HTTP and CONNECT streams have the shared inactivity timeouts described above, without an absolute transfer lifetime. Automatic upstream decompression is disabled to preserve content encoding. Upstream response headers have a 32 KiB bound and a thirty-second timeout, client headers ten seconds and a 32 KiB bound; upstream idle connections are bounded and expire after sixty seconds.

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

Validate native packaging with `systemd-analyze verify packaging/systemd/tailhop.service` when available. A missing /usr/local/bin/tailhop before installation is an expected verification diagnostic; structural validation does not test runtime sandbox compatibility.

Docker image construction and first-run volume permissions must be checked on a host with daemon access. Native service startup, state permissions, journald output, and shutdown must be checked on a systemd host after manual installation. Real enrollment, Tailnet ACL enforcement, observed egress IP, persisted identity and signal shutdown require the manual smoke tests above.
