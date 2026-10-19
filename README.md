# tailhop

> A tiny forward proxy that lets devices on your Tailnet access the internet through a remote host, powered by tsnet.

tailhop is a small Go HTTP forward proxy that joins your Tailnet as its own node.
Configure an application to use it, and that application's HTTP or HTTPS traffic
leaves through the deployment host's normal network route.

```text
Tailnet client → tsnet listener → tailhop → host/container network → internet
```

Unlike a full Tailscale Exit Node, tailhop does not route all traffic from a device.
Applications must explicitly use the proxy. Its embedded tsnet node needs no
separate tailscaled process, TUN device, or public proxy port.

## Features

- HTTP/1.1 forwarding and HTTPS via CONNECT, without TLS interception.
- Streaming uploads and responses, including long-lived active streams.
- Public-IP-only destinations on ports 80 and 443, validated at connection time.
- Global connection admission, activity-based idle timeouts, and graceful shutdown.
- Non-root Docker deployment or a native Linux systemd service, with persistent node identity.

## Quick start

From a checkout of this repository, with Docker and Compose installed:

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env` and replace the `TS_AUTHKEY` placeholder with a Tailscale enrollment
key appropriate for your Tailnet. Allow intended clients to reach this node on
TCP 8080 through your Tailnet grants/ACLs, then start it:

```sh
docker compose config --quiet
docker compose up --build -d
```

Compose builds from local source. Confirm the node is connected in the Tailscale
device view. See the [Docker guide](docs/docker.md) for enrollment, logs, state
volume permissions, updates, and removing the enrollment key after setup.

## Use the proxy

From an authorized Tailnet client, test HTTP and HTTPS:

```sh
curl --proxy http://tailhop:8080 http://example.com/
curl --proxy http://tailhop:8080 https://ifconfig.io/
```

The hostname assumes working MagicDNS and a node named `tailhop`; otherwise use
its actual Tailnet DNS name or IP. If `NO_PROXY` would bypass the proxy, add
`--noproxy ""` to curl (also valid in Windows CMD).

Use the same HTTP proxy address for HTTPS applications: CONNECT carries their TLS
connection. Configure each application explicitly; starting tailhop does not
change the client's default route. The reported public IP follows the actual
host/container egress path, including any NAT, VPN, or routing policy.

## Deployment

- **[Docker / Compose](docs/docker.md):** source-built container with a persistent
  state volume; no published ports or privileged runtime requirements.
- **[Native Linux / systemd](docs/systemd.md):** the same executable under a
  dedicated service account, with systemd-managed state and journald logging.

Both deployments persist tsnet credentials so restarts can reuse the node identity.

## Security considerations

Restrict access with Tailnet grants/ACLs. There is no additional proxy user/password
login, and one client can occupy all connection slots. Defaults are 100 concurrent
inbound TCP connections and a ten-minute inactivity timeout; apply deployment
resource limits appropriate to your workload.

Private and special-use destination addresses are denied, and outbound ports are
restricted to 80 and 443. This policy is not a complete SSRF defense or a content
filter: CONNECT can carry arbitrary TCP on those ports. Ordinary HTTP is plaintext
on the internet leg; HTTPS retains client-to-destination TLS.

See the [security guide](docs/security.md) before exposing access to peers.

## Documentation

- [Docker deployment and troubleshooting](docs/docker.md)
- [Native Linux installation and operation](docs/systemd.md)
- [Configuration, connection limits, and timeouts](docs/configuration.md)
- [Security boundaries and limitations](docs/security.md)

## Contributing and license

Focused fixes, tests, and documentation improvements are welcome. Keep changes
small and preserve the project's tiny forward-proxy scope. [SPEC.md](SPEC.md)
provides the implementation and development reference.

Released under the [MIT license](LICENSE).
