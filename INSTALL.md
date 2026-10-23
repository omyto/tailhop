# Install tailhop on Linux

Use the archive for your Linux machine: `uname -m` reports `x86_64` for amd64 or
`aarch64` for arm64. You need CA certificates and outbound internet connectivity.
No Go, Git, Docker, TUN device, or separate tailscaled process is required.

## Extract and verify

Download one architecture archive from a trusted maintainer. Compare its hash
with the SHA-256 digest displayed for that asset on GitHub (ignore the
`sha256:` prefix when comparing). For example:

```sh
sha256sum tailhop_0.1.0_linux_amd64.tar.gz
```

Use the actual version and architecture in the filename. Stop if the digest
differs. Trust the source of the GitHub digest as well as the download; a matching
hash checks the downloaded bytes, not whether the software itself is safe.
No checksum manifest is distributed. After verifying the archive, extract it:

```sh
mkdir tailhop-install
tar -xzf tailhop_0.1.0_linux_amd64.tar.gz -C tailhop-install
cd tailhop-install
```

Choose direct execution or the systemd service below. Run commands from this
extracted directory. Only the systemd administration commands need sudo.

## Run directly as an unprivileged user

In Bash, configure the process and create private, persistent state:

```bash
umask 077
export TS_HOSTNAME=tailhop
export TS_STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/tailhop"
install -d -m 0700 "$TS_STATE_DIR"
export PROXY_PORT=8080
export LOG_LEVEL=info
export MAX_CLIENTS=100
export IDLE_TIMEOUT=10m
read -r -s -p 'Tailscale Auth Key for initial enrollment: ' TS_AUTHKEY
printf '\n'
export TS_AUTHKEY
./tailhop
unset TS_AUTHKEY
```

The silent prompt avoids putting the key in shell history. Use a Tailscale Auth
Key appropriate for your Tailnet's device approval/tag policy, not an API access
token. Confirm enrollment in the Tailscale device view. Environment variables can
still be inspected by privileged processes and processes with the same UID; do
not dump the environment. Without a key, an uninitialized node may log an
interactive enrollment URL; treat that URL as a secret too.

Press Ctrl-C (SIGINT) to stop gracefully, or send `kill -TERM <pid>` from another
terminal. The proxy drains requests/tunnels for up to ten seconds, then closes
remaining connections. Once the process exits, the example unsets the key.
Restart with the same non-secret environment and state directory:

```bash
unset TS_AUTHKEY
./tailhop
```

Valid enrolled state reuses the node identity without a new key. Keep the state
directory private and persistent; protect backups as credentials. A new shell
needs the same environment exports again, but not the enrollment prompt.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TS_AUTHKEY` | unset | Initial Tailscale enrollment; omit with valid enrolled state. |
| `TS_HOSTNAME` | `tailhop` | Node hostname (DNS label). |
| `TS_STATE_DIR` | `/var/lib/tailhop` | Private writable persistent state; direct execution above overrides this. |
| `PROXY_PORT` | `8080` | Tailnet listener port, 1–65535. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `MAX_CLIENTS` | `100` | Positive global inbound TCP connection limit, not per client. |
| `IDLE_TIMEOUT` | `10m` | Positive Go duration; HTTP/CONNECT inactivity in either direction. |

## Install as a systemd service

Use a dedicated non-login account. If an account/group already exists, confirm
it belongs to tailhop before continuing; do not repurpose an unrelated account.

```sh
getent group tailhop >/dev/null || sudo groupadd --system tailhop
getent passwd tailhop >/dev/null || sudo useradd --system --gid tailhop --home-dir /var/lib/tailhop --no-create-home --shell "$(command -v nologin)" tailhop
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo install -d -o root -g root -m 0700 /etc/tailhop
if ! sudo test -e /etc/tailhop/tailhop.env; then
  sudo install -o root -g root -m 0600 packaging/systemd/tailhop.env.example /etc/tailhop/tailhop.env
fi
sudo chown root:root /etc/tailhop/tailhop.env
sudo chmod 0600 /etc/tailhop/tailhop.env
sudoedit /etc/tailhop/tailhop.env
```

Before starting, replace the `TS_AUTHKEY` placeholder with an enrollment Auth Key,
or remove the assignment when reusing valid enrolled state. Keep systemd
`KEY=value` syntax, without `export` or shell expansion. Never override
`TS_STATE_DIR` here: the unit fixes it at `/var/lib/tailhop` and systemd creates
and owns that directory as tailhop:tailhop with mode 0700.

```sh
sudo install -o root -g root -m 0644 packaging/systemd/tailhop.service /etc/systemd/system/tailhop.service
sudo systemctl daemon-reload
sudo systemctl enable --now tailhop
systemctl status tailhop
sudo journalctl -u tailhop -f
```

After enrollment, remove `TS_AUTHKEY` using `sudoedit` and restart to clear it
from the running environment. The root-only environment file is not a secret
store; the environment exposure caveats above still apply. Do not delete state.
Service activation alone does not confirm completed Tailnet enrollment.

```sh
sudo systemctl restart tailhop
sudo systemctl stop tailhop
sudo systemctl disable tailhop
```

The service runs unprivileged, logs to journald, and uses SIGTERM with a
20-second stop budget. It retries unexpected failures after five seconds.
Switching from direct execution or Docker does not automatically migrate that
installation's tsnet identity; the systemd state directory is separate.

## Verify connectivity

From an authorized, connected Tailnet client:

```sh
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

`tailhop` requires working MagicDNS; otherwise use the actual Tailnet DNS name or
IP. Tailnet grants/ACLs must allow intended clients to reach TCP 8080 (or your
configured port). Applications explicitly use the HTTP proxy, including for
HTTPS. Add `--noproxy ""` to curl if `NO_PROXY` would bypass it.

Inbound traffic arrives through tsnet, with no public port to expose. Outbound
traffic follows the host's normal route. Destinations must be public IPs on
ports 80/443. There is no additional proxy username/password authentication;
one peer can consume the global connection limit. Restrict access accordingly.

## Update or uninstall

For an update, verify the new archive against its GitHub asset digest, then
extract it into a fresh directory as above. Then, from that directory:

```sh
sudo systemctl stop tailhop
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo systemctl restart tailhop
systemctl status tailhop
sudo journalctl -u tailhop -n 50 --no-pager
```

Keep `/etc/tailhop/tailhop.env` and `/var/lib/tailhop` intact. If updating the
unit too, install the bundled unit and run `sudo systemctl daemon-reload` before
restarting. Repeat the curl check and confirm the same Tailscale device identity.
For direct execution, stop the old process, replace its executable, and restart
with the same state path and non-secret environment.

To uninstall the systemd service and binary:

```sh
sudo systemctl disable --now tailhop
sudo rm -f /etc/systemd/system/tailhop.service /usr/local/bin/tailhop
sudo systemctl daemon-reload
```

These commands intentionally retain configuration, state, and the service
account. Removing state can require enrollment again and create a new identity.
