# Configuration and connection lifecycle

Tailhop uses Go's standard flags and validates configuration before startup:

```text
CLI flags > non-empty environment variables > defaults
```

Explicit flags win even when equal to a built-in default. Unset or empty
environment variables use defaults. `./tailhop --help` (or `-h`) exits successfully
without validating environment configuration, creating directories, or starting tsnet.
Unknown flags, positional arguments, and invalid configuration fail with a non-zero exit.

| CLI flag | Environment variable | Default | Accepted values / meaning |
| --- | --- | --- | --- |
| `--hostname` | `TS_HOSTNAME` | `tailhop` | DNS label: 1–63 ASCII letters, digits, hyphens; no leading/trailing hyphen. |
| `--port` | `PROXY_PORT` | `8080` | TCP port 1–65535 on the Tailnet; destination ports stay fixed. |
| `--state-dir` | `TS_STATE_DIR` | XDG/home path below | Non-empty private writable persistent tsnet directory. |
| `--log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `--max-clients` | `MAX_CLIENTS` | `100` | Positive integer; global inbound TCP connection limit. |
| `--idle-timeout` | `IDLE_TIMEOUT` | `10m` | Positive Go duration such as `30s`, `10m`, `2h`. |
| None | `TS_AUTHKEY` | unset | Initial enrollment key; omit with valid enrolled state. Never pass as a CLI argument. |

For direct execution, use flags:

```bash
./tailhop --hostname tailhop-home --port 8080
MAX_CLIENTS=100 ./tailhop --max-clients 200
```

The last command uses 200. State resolution is `--state-dir`, then non-empty
`TS_STATE_DIR`, then `$XDG_STATE_HOME/tailhop` if XDG_STATE_HOME is absolute and
valid, otherwise the current user's home from Go's `os.UserHomeDir` plus
`.local/state/tailhop`. Relative or invalid XDG paths are ignored. A missing or
invalid home requires an explicit state path. No default depends on the working directory.

Keep state private (0700), persistent, and writable by the runtime user. Existing
state is retained; no automatic discovery or migration takes place. **Native upgrades
from the old `/var/lib/tailhop` default must keep that path explicitly**, with
`--state-dir /var/lib/tailhop` or `TS_STATE_DIR=/var/lib/tailhop`, to reuse identity.
A new path may create a new Tailscale node. Restart using the same state directory
without TS_AUTHKEY after enrollment; see [INSTALL.md](../INSTALL.md).

Docker's recommended `compose.release.yaml` pulls `omyto/tailhop` using the
required `TAILHOP_VERSION` from Compose's `.env` (or shell environment); there is
no version fallback or end-user Docker Hub username setting. `TAILHOP_VERSION`
selects the image and is not read by Tailhop. The development `compose.yaml`
builds local source instead. Both use the same application environment variables.
Docker sets `TS_STATE_DIR=/var/lib/tailhop` and persists it in a volume. The
packaged systemd unit fixes `--state-dir=/var/lib/tailhop`, so an environment
variable cannot override it; `StateDirectory=tailhop` manages ownership.

Tailhop does not automatically parse `.env` or any configuration file. Compose's
`.env` interpolation and systemd's optional `EnvironmentFile` are deployment
mechanisms that supply environment variables to the process. systemd syntax is
`KEY=value`, without `export` or shell/Compose expansion. Do not commit environment
files or state. See [Docker](docker.md) and [systemd](systemd.md).

## Connection admission

The resource model is inspired by Tinyproxy's MaxClients 100 and Timeout 600, using two settings rather than quotas or a worker pool. `MAX_CLIENTS` counts live **inbound TCP connections**, globally across all Tailnet source IPs. A connection holds one slot from admission until full close, including:

- Idle HTTP keep-alive and active HTTP forwarding.
- Pending CONNECT DNS lookups and upstream dials.
- Established CONNECT tunnels, including after a TCP half-close.

Keep-alive reuse does not acquire another slot. Full close releases capacity exactly once on normal completion, error, timeout, or shutdown. A failed request may retain its slot while its HTTP connection stays open.

At capacity, the listener accepts and immediately closes excess sockets, including those that have not sent headers. There is no HTTP rejection response, queue, or rejection goroutine. Clients may see EOF, reset, or a proxy connection error; a later retry can succeed after capacity becomes available.

One client can consume all slots. This is not per-client fairness, bandwidth control, or a complete memory/CPU limit. Restrict Tailnet grants/ACLs and apply deployment resource limits; see [security](security.md#resource-exhaustion).

## Inactivity and streaming

`IDLE_TIMEOUT` covers HTTP keep-alive, active HTTP forwarding, and CONNECT. Ordinary HTTP connections share one inactivity timer across successful reads and writes on the inbound socket. Activity in either direction resets it. Stalled request bodies and response streams close when neither direction transfers data for the interval, canceling blocked forwarding operations and releasing the slot.

Continuous one-way uploads/downloads and streaming/SSE remain connected while data transfers continue. There is no absolute transfer lifetime. A timeout can appear as EOF, reset, or a truncated response rather than an HTTP timeout response. The timer observes socket transfers, not application-level progress or data still buffered elsewhere.

HTTP keep-alive retains its server idle timeout. The fixed ten-second header deadline remains effective even when partial header traffic resets inactivity. There are no absolute body read/write deadlines or per-connection monitoring goroutines; expiration rechecks current activity to avoid stale callbacks closing reused connections.

CONNECT disables the HTTP timer before its bounded DNS/dial operation and uses its shared tunnel timer after hijacking. Successful forwarded writes to either peer reset tunnel inactivity; an inactive or stalled tunnel closes both peers. TCP half-close and early buffered bytes are preserved. Rejected CONNECT requests returning to HTTP keep-alive resume HTTP tracking. These are explicit socket/tunnel inactivity rules rather than a claim of identical Tinyproxy internals.

## Forwarding and other timeouts

Ordinary HTTP uses absolute-form `http://` URLs; HTTPS uses CONNECT. Methods, bodies, status, and end-to-end headers stream without buffering entire transfers. Responses flush on each write for low-latency delivery. Redirects are returned to the client, Host is normalized from the target URL, and automatic upstream decompression is disabled to preserve content encoding. Hop-by-hop headers, proxy credentials, and trailers are not forwarded.

Destination ports are fixed at **80 and 443**. The standard outbound transport ignores environment `HTTP_PROXY`/`HTTPS_PROXY`; it uses the deployment's normal network route.

| Operation | Bound |
| --- | --- |
| Client request headers | 10 seconds; configured 32 KiB header limit |
| DNS and all outbound dial attempts together | 10 seconds |
| Upstream HTTP response headers | 30 seconds; 32 KiB limit |
| Pooled upstream HTTP idle connection | 60 seconds; 64 idle connections total, 4 per host |

Allowed, deduplicated IPv4/IPv6 candidates are interleaved in resolver order within each family. Sequential non-final attempts receive at most two seconds and at most their fair share of the remaining budget; the final attempt receives the remainder. Every attempt dials a validated numeric IP, with no hostname re-resolution or parallel connection races. See [destination security](security.md#outbound-destinations).

## Shutdown

SIGINT/SIGTERM stop admission and immediately cancel pending CONNECT DNS/dials, preventing late tunnel registration. Existing HTTP streams and CONNECT tunnels share a ten-second graceful drain budget. Pending HTTP dials respect client cancellation, including transport pooling behavior.

At expiry, outbound HTTP operations are canceled and remaining inbound connections and tunnels are force-closed. Completion is observed within the budget; cleanup already in progress after forced closure is not waited on beyond it. Idle upstream connections are closed and tsnet is shut down afterward. Compose and systemd each allow twenty seconds before forced process termination.

## Logging

Structured application logs include source IP from the tsnet connection, method, validated destination hostname, status, and duration as applicable. Forwarding headers are not trusted for identity, and no per-request WhoIs lookup is used. Request bodies, URL queries, proxy credentials, and sensitive header values are excluded.

Verbose tsnet backend output is suppressed; user status/enrollment messages use structured logging. Tailscale's library also manages its own operational log uploads. An interactive enrollment URL may appear for an uninitialized node without an auth key; treat it as a secret.
