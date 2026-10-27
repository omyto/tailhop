# tailhop

tailhop is a lightweight HTTP/HTTPS forward proxy powered by `tsnet`. It runs as a standalone Tailnet node, letting apps configured to use it access the internet through the host's connection—without routing all device traffic like a Tailscale exit node.

```text
Tailnet client ──► tailhop (tsnet) ──► Host/container egress ──► Internet
```

## Features

- Tailnet-native HTTP forwarding and HTTPS CONNECT tunneling.
- Streaming uploads and responses without TLS interception.
- Embedded Tailscale networking; no separate `tailscaled` daemon or public proxy port.
- Non-root Docker and native Linux systemd deployments with persistent node identity.
- Public-IP and destination-port restrictions, global connection limits, and idle timeouts.

## Quick start

From a checkout of this repository, with Docker and Compose installed:

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env` and replace `TS_AUTHKEY` with a Tailscale Auth Key for initial
node enrollment (not an API access token). Allow intended clients to reach this
node on TCP 8080 through your Tailnet grants/ACLs, then start it:

```sh
docker compose config --quiet
docker compose up --build -d
docker compose ps
docker compose logs --tail=50
```

Compose builds from local source. Confirm the node appears in the Tailscale admin console. See the [Docker guide](docs/docker.md) for detailed setup and state persistence.

## Use the proxy

From an authorized Tailnet client, test HTTP and HTTPS:

```sh
curl --proxy http://tailhop:8080 http://ifconfig.io/
curl --proxy http://tailhop:8080 https://ifconfig.io/
```

The name `tailhop` assumes working MagicDNS; otherwise use the node's actual
Tailnet DNS name or IP. If `NO_PROXY` would bypass the proxy, add
`--noproxy ""` to curl (also valid in Windows CMD).

The reported public IP reflects the deployment's egress path, including any NAT,
VPN, or routing policy.

## Security considerations

Tailnet grants/ACLs control inbound access; tailhop has no proxy username/password
authentication. Outbound destinations are restricted to public IPs on ports 80 and
443. These restrictions are not a complete SSRF defense or a protocol filter.

Connection limits are global, not per client: one peer can occupy all slots.
Defaults are 100 inbound TCP connections and a 10-minute idle timeout.
Set deployment resource limits for your workload. Ordinary HTTP is plaintext
on the internet leg; HTTPS retains client-to-destination TLS.

See the [security guide](docs/security.md) for boundaries, risks, and limitations.

## Deployment and documentation

- [Docker / Compose](docs/docker.md): source builds and versioned image deployment,
  with persistent state and no published ports or privileged runtime requirements.
- [Linux binary installation](INSTALL.md): run directly or install as a systemd service.
  The [systemd guide](docs/systemd.md) covers private builds and source installation.
- [Configuration](docs/configuration.md): environment variables, connection limits,
  and timeouts.

## Contributing and license

Focused fixes, tests, and documentation improvements are welcome. Read the
[contributing guide](CONTRIBUTING.md) for build commands, tests, and pull request
expectations. [SPEC.md](SPEC.md) provides the implementation reference.

Released under the [MIT license](LICENSE).
