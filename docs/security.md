# Security and limitations

tailhop is a tiny, explicit forward proxy for trusted Tailnet peers to reach public destinations. It is not a complete SSRF defense, content filter, or whole-device exit node. Applications must opt in to using it.

## Inbound authorization

Tailnet grants/ACLs are the authorization boundary. Restrict access to intended peers on the configured proxy port (8080 by default). Inbound connections arrive only through tsnet; no public proxy port or separate tailscaled daemon is needed.

There is no additional username/password authentication or per-user quota. Logging uses the source IP of the tsnet connection, not client-supplied forwarding headers; it does not perform per-request WhoIs lookups.

## Outbound destinations

Both HTTP forwarding and CONNECT permit destination ports **80 and 443 only**. Every new upstream TCP connection validates its destination at the dialing boundary. DNS answers are filtered and deduplicated, then a vetted **numeric IP** is dialed rather than resolving the hostname again. This prevents DNS rebinding between validation and connection. Reused HTTP connections remain pinned to the already vetted peer; mixed DNS answers use only permitted addresses.

The policy rejects loopback, private, link-local, CGNAT, unspecified, multicast, broadcast, metadata, documentation, benchmarking, and reserved/special-use ranges. IPv4-mapped IPv6 is normalized before classification, preventing mapped representations from bypassing IPv4 restrictions. IPv6 is accepted only within 2000::/3 minus conservative special-purpose and transition-space exclusions. Public exceptions inside excluded prefixes are deliberately denied.

See the [policy implementation](../internal/proxy/policy.go) and [IANA IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) / [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) registries when reviewing exclusions. Classification cannot compensate for a host that routes nominally public IPs to sensitive internal services. A reachable public service can itself relay traffic elsewhere. Apply appropriate host/network egress controls for your environment.

Outbound connections use the host/container's normal network stack and route, not tsnet dialing. The forwarding transport does not inherit `HTTP_PROXY`/`HTTPS_PROXY` settings. Docker bridge networking usually adds host/upstream NAT; VPNs, cloud networking, and routing policy can change the actual egress path.

## Encryption and protocol boundaries

Tailscale protects the Tailnet leg. Ordinary HTTP remains plaintext between tailhop and the destination. HTTPS through CONNECT retains the client's end-to-end TLS without interception; certificate verification remains the client's responsibility.

CONNECT tunnels arbitrary TCP streams on permitted ports without inspecting their contents. Port restrictions therefore do not enforce HTTP/TLS-only traffic. Hop-by-hop headers and proxy credentials are stripped from ordinary HTTP forwarding, but this does not make untrusted destinations safe.

There is no SOCKS, UDP/QUIC forwarding, HTTP/2 CONNECT, HTTPS proxy listener, Upgrade/WebSocket forwarding, reverse proxying, TLS interception, caching, dashboard, private-subnet access, or automatic device-wide routing.

## Secrets and state

Interactive enrollment requires no key or configuration file. If using a key, choose one appropriate for your Tailnet's approval/tag policy. `--auth-key` is optional, not a secret-management mechanism: literal arguments can appear in shell history, process listings, and container metadata. Remove the flag after enrollment. Keep any legacy environment files private and out of version control. Environment variables are not a secret store: privileged operators, processes sharing the service UID, container administrators, and diagnostic tooling may expose them. Avoid printing resolved Compose configuration; use `docker compose config --quiet`.

After enrollment, remove `TS_AUTHKEY` and recreate the container or restart the service to clear the running environment. Valid persisted tsnet state allows reuse of the node identity without a new key. An uninitialized node without a key may log an interactive enrollment URL; protect that URL as a secret.

State contains node credentials. Keep `/var/lib/tailhop` private (mode 0700), owned by the runtime user, and persistent across updates. Protect backups as secrets too. Deleting state may require enrollment again and create a new identity. Do not store it in a temporary directory or commit it. See [Docker](docker.md) and [systemd](systemd.md) for ownership details.

## Resource exhaustion

`MAX_CLIENTS=100` is a global inbound TCP connection limit, not per-client fairness. One peer can occupy every slot, including with idle keep-alive or pending CONNECT requests. Excess connections are closed promptly without an HTTP response.

`IDLE_TIMEOUT=10m` releases inactive HTTP connections and tunnels, including stalled HTTP streams. Successful traffic in either direction resets inactivity, so active transfers can continue indefinitely. There is no per-client quota, rate limit, bandwidth cap, or absolute transfer lifetime. The connection limit alone does not bound all memory, CPU, or process resources.

Use restrictive grants/ACLs and appropriate container resource limits or systemd drop-ins. Tune limits for tsnet and the intended workload. See [connection lifecycle and timeouts](configuration.md).

## Verification boundaries

Credential-free tests exercise local forwarding, policy, cancellation, and lifecycle behavior. They do not establish live enrollment, ACL enforcement, deployment permissions, observed egress IP, or host sandbox compatibility. Run the [Docker smoke checks](docker.md#manual-verification) or [native service checks](systemd.md#verify-and-operate) on the actual deployment.
