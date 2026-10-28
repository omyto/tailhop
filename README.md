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

Use the official [Docker Hub image](https://hub.docker.com/r/omyto/tailhop).

For Docker, use `TS_AUTHKEY` for initial enrollment and keep the state volume.
Browser enrollment through logs is an alternative when you do not want to use a key.

1. Create a Tailscale auth key for the intended Tailnet, then run in Bash:

   ```bash
   read -r -s -p 'Tailscale auth key: ' TS_AUTHKEY
   printf '\n'
   export TS_AUTHKEY
   docker run -d \
     --name tailhop \
     --restart unless-stopped \
     --stop-timeout 20 \
     --cap-drop ALL \
     --security-opt no-new-privileges \
     -e TS_AUTHKEY \
     -v tailhop-state:/var/lib/tailhop \
     omyto/tailhop:latest
   unset TS_AUTHKEY
   ```

   The named volume `tailhop-state` preserves the Tailscale identity across
   container recreation. Keep it; deleting it can require enrollment again.

2. Confirm the node is connected in the Tailscale device view and approve it if
   required. A running container or a `proxy listening` log alone does not mean
   enrollment is complete. After enrollment, stop and remove the container, then
   repeat the run command **without `-e TS_AUTHKEY`**, keeping the same volume.
   Unsetting the shell variable alone does not clear the container's environment.

   The prompt keeps the key out of shell history, but Docker administrators can
   inspect container environment values. See the [Docker guide](docs/docker.md)
   for complete cleanup and Compose instructions.

3. From an authorized Tailnet client, verify the proxy:

   ```bash
   curl --proxy http://tailhop:8080 https://ifconfig.me/ip
   ```

   `tailhop` is the default Tailscale hostname. Use its Tailnet IP if MagicDNS is
   unavailable, and allow TCP 8080 through Tailnet grants/ACLs. The response should
   show the public egress IP used by the Tailhop host/container.

See the [Docker guide](docs/docker.md) for auth-key and browser enrollment,
configuration, Compose, upgrades, and source development.

## Run the Linux binary

Download the matching Linux archive from [GitHub Releases](https://github.com/omyto/tailhop/releases),
verify it as described in [INSTALL.md](INSTALL.md), then extract it and run:

```bash
./tailhop --help
./tailhop
./tailhop --hostname tailhop-home --port 8080
```

Native state defaults to `$XDG_STATE_HOME/tailhop` when absolute, otherwise
`$HOME/.local/state/tailhop`. Flags override environment variables, then defaults.
With no key, open the logged URL to enroll interactively. Optional `--auth-key`
and legacy `TS_AUTHKEY` are also supported; see [INSTALL.md](INSTALL.md) for key
exposure risks and systemd installation.
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

## Limitations

Public destinations on ports 80 and 443 only. No SOCKS, UDP/QUIC, TLS interception,
or per-user quotas; applications must explicitly use the HTTP proxy.

## Deployment and documentation

- [Docker / Compose](docs/docker.md): prebuilt images, upgrades, and source development,
  with persistent state and no published ports or privileged runtime requirements.
- [Linux binary installation](INSTALL.md): run directly or install as a systemd service.
  The [systemd guide](docs/systemd.md) covers service setup and source installation.
- [Configuration](docs/configuration.md): CLI flags, environment fallback, connection limits,
  and timeouts.

## Contributing and license

Focused fixes, tests, and documentation improvements are welcome. Read the
[contributing guide](CONTRIBUTING.md) for build commands, tests, and pull request
expectations. [SPEC.md](SPEC.md) provides the implementation reference.

Released under the [MIT license](LICENSE). Third-party terms are retained in
[THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).
