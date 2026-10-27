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

Use the official [Docker Hub image](https://hub.docker.com/r/omyto/tailhop)
with Docker and Compose installed; no source checkout or Go toolchain is needed.
Obtain `compose.release.yaml` and `.env.example` from the maintainer and place
both in a new deployment directory. Run from that directory:

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env`: set `TAILHOP_VERSION` to an explicitly selected published tag from
Docker Hub (there is no `latest` fallback), and replace `TS_AUTHKEY` with a
Tailscale Auth Key for initial enrollment, not an API access token. Allow intended
clients through Tailnet grants/ACLs on TCP 8080, then start:

```sh
docker compose -f compose.release.yaml config --quiet
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d
docker compose -f compose.release.yaml ps
docker compose -f compose.release.yaml logs --tail=50
```

Confirm the node in the Tailscale admin console. Remove `TS_AUTHKEY` from `.env`
(and unset it in your shell if exported), then clear the container's environment:

```sh
docker compose -f compose.release.yaml up -d --force-recreate
```

For upgrades, change `TAILHOP_VERSION` in `.env`, pull, and recreate the container
using the same project and state volume. Do not use `docker compose down -v`:
it can delete the persisted Tailscale identity. See the [Docker guide](docs/docker.md)
for upgrade commands and [source development](docs/docker.md#development-build-from-source).

## Run the Linux binary

Extract a verified Linux release archive, then:

```bash
./tailhop --help
./tailhop
./tailhop --hostname tailhop-home --port 8080
```

Native state defaults to `$XDG_STATE_HOME/tailhop` when absolute, otherwise
`$HOME/.local/state/tailhop`. Flags override environment variables, then defaults.
For initial key enrollment, supply `TS_AUTHKEY` securely through the environment;
see [INSTALL.md](INSTALL.md) for a silent prompt and systemd installation.
Restart with the same state directory to reuse identity without a new key.
Existing native users of `/var/lib/tailhop` should keep it explicitly with
`--state-dir /var/lib/tailhop`; state is never migrated automatically.
Tailhop does not load `.env` files; Compose supplies its environment.

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

- [Docker / Compose](docs/docker.md): prebuilt images, upgrades, and source development,
  with persistent state and no published ports or privileged runtime requirements.
- [Linux binary installation](INSTALL.md): run directly or install as a systemd service.
  The [systemd guide](docs/systemd.md) covers private builds and source installation.
- [Configuration](docs/configuration.md): CLI flags, environment fallback, connection limits,
  and timeouts.

## Contributing and license

Focused fixes, tests, and documentation improvements are welcome. Read the
[contributing guide](CONTRIBUTING.md) for build commands, tests, and pull request
expectations. [SPEC.md](SPEC.md) provides the implementation reference.

Released under the [MIT license](LICENSE).
