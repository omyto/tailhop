# Configuration and connection lifecycle

Tailhop uses Go's standard flags and validates configuration before startup:

```text
CLI flags > non-empty environment variables > defaults
```

Explicit flags win even when equal to a built-in default. Unset or empty
environment variables use defaults. `./tailhop --help` (or `-h`) exits successfully
without validating environment configuration, creating directories, or starting tsnet.
Unknown flags, positional arguments, and invalid configuration fail with a non-zero exit.

`./tailhop --version` prints the version, source commit and Go toolchain, then
exits successfully without reading environment configuration, creating state or
starting tsnet. Release binaries and images embed the validated version/commit.
Local source builds report `dev`, with an embedded VCS revision when available
and `-dirty` for modified source, otherwise `unknown`. Unstamped Docker builds
report `dev` and `unknown`; builders can supply `VERSION` and `COMMIT` build args.

| CLI flag | Environment variable | Default | Accepted values / meaning |
| --- | --- | --- | --- |
| `--hostname` | `TS_HOSTNAME` | `tailhop` | DNS label: 1–63 ASCII letters, digits, hyphens; no leading/trailing hyphen. |
| `--port` | `PROXY_PORT` | `8080` | TCP port 1–65535 on the Tailnet; destination ports stay fixed. |
| `--state-dir` | `TS_STATE_DIR` | XDG/home path below | Non-empty private writable persistent tsnet directory. |
| `--log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` for application logs; startup/enrollment output remains visible. |
| `--max-clients` | `MAX_CLIENTS` | `100` | Positive integer; global inbound TCP connection limit. |
| `--idle-timeout` | `IDLE_TIMEOUT` | `10m` | Positive Go duration such as `30s`, `10m`, `2h`. |
| `--auth-key` | `TS_AUTHKEY` | empty | Optional initial enrollment/reauthentication key; browser login when no credentials or valid state are available. |

No configuration file is required. CLI flags are recommended for direct execution;
environment variables remain supported for automation and backward compatibility.
Auth-key precedence is explicit `--auth-key`, then non-empty `TS_AUTHKEY`, then
legacy `TS_AUTH_KEY`. `--auth-key=` disables both environment auth-key fallbacks;
omitting the flag preserves them. This does not disable independent OAuth or
identity-federation credentials. With no credentials, tsnet starts interactive
login when necessary; valid persisted state needs no new key.

For manual setup, open the login URL from the logs; no auth key is required.
For automation, supply `TS_AUTHKEY` for enrollment. This choice applies to Docker
and native deployments alike. Keep valid state to avoid enrolling again.

Never put an auth key in a permanent service command. CLI keys can appear in shell
history, process listings and Docker metadata; the flag is not secret management.
Environment variables can also be inspected by privileged operators.

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

The [Docker guide](docker.md) starts with `docker run`. Both repository Compose
files also work without `.env`: `compose.release.yaml` uses `omyto/tailhop:latest`,
while `compose.yaml` builds local source. Pin a version by editing `image:`.
Use Compose `command` for optional CLI settings, or an explicit `environment`
mapping for legacy variables. Compose does not automatically forward the host's
application environment; prior `.env` users must transfer needed settings to
`command` or `environment`. Existing files are not deleted automatically.
Docker sets `TS_STATE_DIR=/var/lib/tailhop` in the image and persists that path in
a volume. The packaged systemd unit fixes `--state-dir=/var/lib/tailhop`, which
wins over `TS_STATE_DIR`; `StateDirectory=tailhop` manages ownership.

Tailhop does not automatically parse `.env` or any application configuration file.
Compose interpolation and systemd's optional `EnvironmentFile` are deployment
mechanisms, not application parsers. No file is needed for standard deployments.
See [Docker](docker.md), [systemd](systemd.md), and [secrets and state](security.md#secrets-and-state).

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

CONNECT disables the HTTP timer before its bounded DNS/dial operation and uses its shared tunnel timer after hijacking. Successful forwarded writes to either peer reset tunnel inactivity; an inactive or stalled tunnel closes both peers. TCP half-close and early buffered bytes are preserved. The patched Go HTTP server closes the client connection after a rejected CONNECT; clients must reconnect before sending another request. These are explicit socket/tunnel inactivity rules rather than a claim of identical Tinyproxy internals.

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

Each hijacked CONNECT connection emits one `tunnel closed` event with a `duration`
from hijacking through cleanup (including the CONNECT response, excluding DNS and
dialing). JSON durations use nanoseconds, as with HTTP request durations. The
`reason` is `eof` for normal completion or the safe half-close fallback,
`io_error` for transfer/half-close errors, `idle_timeout` for inactivity,
`shutdown` for forced closure after the drain budget, or `handshake_error` if
sending the CONNECT response fails. The first cause to claim closure is retained;
errors caused by cleanup do not overwrite it. Gracefully drained tunnels keep
their natural completion reason. Raw I/O error text and payloads are not logged.

The `tailnet ready` event with `state=Running` comes from tsnet's state API,
including the initial state of an already enrolled node. The installer uses this
application event to recognize readiness; `proxy listening` alone is not readiness.
The state watcher stops after reporting readiness or when the process shuts down.

Verbose tsnet backend output is suppressed. User status/enrollment messages and
the `tailnet ready` event use structured startup logging independently of
`LOG_LEVEL` / `--log-level`, so `warn` and `error` do not hide browser login
instructions or readiness from the installer. Ordinary application/request logs
still respect the configured level. Auth keys are redacted from tsnet user
messages. Tailscale's library also manages its own operational log uploads.
An interactive enrollment URL may appear for an uninitialized node without an
auth key; treat it as a secret.
