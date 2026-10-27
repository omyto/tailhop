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

Inspect flags, then run with defaults or customize the hostname and port:

```bash
./tailhop --help
./tailhop
./tailhop --hostname tailhop-home --port 8080
```

The default state directory is `$XDG_STATE_HOME/tailhop` when `XDG_STATE_HOME`
is absolute, otherwise `$HOME/.local/state/tailhop`. Tailhop creates it privately
with mode 0700 and checks writability. Relative or invalid XDG paths fall back to
the home directory. The working directory never determines the default.

For first-time enrollment using a key, in Bash:

```bash
read -r -s -p 'Tailscale Auth Key for initial enrollment: ' TS_AUTHKEY
printf '\n'
export TS_AUTHKEY
./tailhop --hostname tailhop-home --port 8080
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
Restart with the same flags and state directory:

```bash
unset TS_AUTHKEY
./tailhop --hostname tailhop-home --port 8080
```

Valid enrolled state reuses the node identity without a new key. Keep the state
directory private and persistent; protect backups as credentials. Customize other
settings with flags, for example:

```bash
./tailhop \
  --state-dir "$HOME/.local/state/tailhop" \
  --max-clients 200 \
  --idle-timeout 15m
```

CLI flags take precedence over non-empty environment variables, then defaults.
`TS_AUTHKEY` is environment-only; there is no auth-key flag. Tailhop does not
parse `.env` files. The six flags are `--hostname`, `--port`, `--state-dir`,
`--log-level`, `--max-clients`, and `--idle-timeout`; see `./tailhop --help`
for defaults and accepted values. `-h` also displays help without starting tsnet.

**Upgrading an existing native installation:** the old default was
`/var/lib/tailhop`. Keep using it with `./tailhop --state-dir /var/lib/tailhop`
or `TS_STATE_DIR=/var/lib/tailhop ./tailhop` to preserve identity (the runtime
user still needs access). Tailhop does not discover or migrate old state.
Using a different directory may enroll a new node; it never deletes old state.
Docker and the packaged systemd service continue using `/var/lib/tailhop`.

## Install as a systemd service

Use a dedicated non-login account. If an account/group already exists, confirm
it belongs to tailhop before continuing; do not repurpose an unrelated account.
Install the binary and unit without creating an environment file:

```sh
getent group tailhop >/dev/null || sudo groupadd --system tailhop
getent passwd tailhop >/dev/null || sudo useradd --system --gid tailhop --home-dir /var/lib/tailhop --no-create-home --shell "$(command -v nologin)" tailhop
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo install -o root -g root -m 0644 packaging/systemd/tailhop.service /etc/systemd/system/tailhop.service
sudo systemctl daemon-reload
```

At startup, `StateDirectory=tailhop` creates and owns `/var/lib/tailhop` as
`tailhop:tailhop` with mode 0700. State persists across restarts and binary updates.
The unit fixes `--state-dir=/var/lib/tailhop`, taking precedence over `TS_STATE_DIR`.
Do not delete or replace existing state; installation does not migrate it.
For direct execution users, follow [the migration guide](#migrate-from-direct-execution-to-systemd)
before starting the service.

### Optional initial enrollment and configuration

Create `/etc/tailhop/tailhop.env` **only** when supplying an initial `TS_AUTHKEY`
or overriding defaults. For key-based first-time enrollment, these commands
preserve any existing file and protect it as root:root with mode 0600:

```sh
sudo install -d -o root -g root -m 0700 /etc/tailhop
sudo sh -c 'umask 077; cp --no-clobber "$1" "$2"' sh packaging/systemd/tailhop.env.example /etc/tailhop/tailhop.env
sudo chown root:root /etc/tailhop/tailhop.env
sudo chmod 0600 /etc/tailhop/tailhop.env
sudoedit /etc/tailhop/tailhop.env
```

Replace the placeholder with an appropriate Tailscale enrollment Auth Key,
not an API access token. For configuration overrides only, remove the `TS_AUTHKEY`
assignment instead. Use systemd `KEY=value` syntax, without `export` or shell/Compose
expansion. Optional overrides include `TS_HOSTNAME`, `PROXY_PORT`, `LOG_LEVEL`,
`MAX_CLIENTS`, and `IDLE_TIMEOUT`; `TS_STATE_DIR` cannot override the unit's flag.
Tailhop does not parse this file or `.env` itself.

Start the service below and confirm enrollment in the Tailscale device view.
After successful enrollment, remove `TS_AUTHKEY` with `sudoedit` and restart the
service to clear it from the running environment. Valid persisted state normally
needs no new key. If no overrides remain, the environment file can remain absent.
Environment variables are not a secret store; avoid dumping them, and protect any
interactive enrollment URL in the journal. Without a key, an uninitialized node
may offer interactive login instead.

### Start and operate

An already enrolled instance can start **without** `/etc/tailhop/tailhop.env`
when valid state exists in `/var/lib/tailhop`, the service user can read and write
it, and no required overrides depend on the file. The unit uses
`EnvironmentFile=-/etc/tailhop/tailhop.env`, so a missing file is allowed. With
built-in defaults sufficient, skip the optional setup above.

```sh
sudo systemctl enable --now tailhop
systemctl status tailhop
sudo journalctl -u tailhop -f
```

Service activation alone does not confirm completed Tailnet enrollment. Keep
state intact when restarting or updating:

```sh
sudo systemctl restart tailhop
sudo systemctl stop tailhop
sudo systemctl disable tailhop
```

The service runs unprivileged, logs to journald, and uses SIGTERM with a
20-second stop budget. It retries unexpected failures after five seconds.
Switching from direct execution or Docker does not automatically migrate that
installation's tsnet identity; the systemd state directory is separate.

## Migrate from direct execution to systemd

Tailscale node identity lives in the persisted tsnet state directory. Copying
valid enrolled state lets the packaged service reuse the same node and Tailnet
IP without a new `TS_AUTHKEY`. Direct execution normally uses absolute
`$XDG_STATE_HOME/tailhop`, otherwise `~/.local/state/tailhop` (including when XDG
is unset or relative). The service always uses `/var/lib/tailhop`.

First record the existing device identity and Tailnet IP in the Tailscale admin
console, plus the hostname, port, and other settings used by the direct process.
Prepare the dedicated account, binary, and unit using
[Install as a systemd service](#install-as-a-systemd-service), but **do not run
`enable --now` or start the service yet**. Skip enrollment-key setup when reusing
state. If you copied the example environment file, remove its `TS_AUTHKEY`
assignment with `sudoedit`; do not overwrite an existing environment file.

Stop the direct process with Ctrl-C, or `kill -TERM <pid>` using its known PID.
Wait for it to exit fully, including the ten-second proxy drain and tsnet cleanup;
verify that PID is gone before copying. Disable any launcher that would restart
it. Never run both copies concurrently, even from different directories: they
represent the same Tailscale identity.

In Bash, as the original runtime user, identify the actual source. The following
suggestion uses **this shell's** environment; check the original invocation:
`--state-dir` wins over `TS_STATE_DIR`, then XDG/home defaults. Enter the absolute
path if an override was used. Resolve a relative override against the original process's
working directory, not this shell's. Do not dump the process environment or state
files, which may contain credentials.

```bash
if [ -n "${TS_STATE_DIR:-}" ]; then
  suggested_state=$TS_STATE_DIR
elif [[ ${XDG_STATE_HOME:-} = /* ]]; then
  suggested_state=${XDG_STATE_HOME%/}/tailhop
else
  suggested_state=$HOME/.local/state/tailhop
fi
read -r -p "Actual original state directory [$suggested_state]: " source_state
source_state=${source_state:-$suggested_state}
```

With both instances stopped, copy the complete contents, including hidden files.
Run this block only after confirming the direct process has exited. It stops the
service too, rejects an existing destination identity, and leaves the source
unchanged. Do not start either instance or run another migration during the copy.

```bash
sudo systemctl stop tailhop
sudo bash -s -- "$source_state" <<'MIGRATE'
set -euo pipefail
source_state=$(realpath -e -- "$1")
destination=/var/lib/tailhop
fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$(systemctl show -p ActiveState --value tailhop)" = inactive ] ||
  fail 'Service is not inactive; stop and investigate.'
[ -d "$source_state" ] || fail 'Source is not a directory.'
[ -n "$(find "$source_state" -mindepth 1 -printf x -quit)" ] ||
  fail 'Source is empty; locate the enrolled state before continuing.'
[ "$source_state" != "$destination" ] ||
  fail 'Source already uses the service path; do not copy it onto itself.'
[ "$source_state" != / ] || fail 'Source must be a dedicated state directory.'
case "$destination" in
  "$source_state"/*) fail 'Destination is inside the source; review manually.' ;;
esac
# Normal tsnet state contains regular files and directories. Review anything else.
[ -z "$(find "$source_state" -mindepth 1 ! -type d ! -type f -printf x -quit)" ] ||
  fail 'Source contains symlinks or special files; review manually before copying.'
[ ! -L "$destination" ] || fail 'Destination is a symlink; review manually.'
if [ -e "$destination" ]; then
  [ -d "$destination" ] || fail 'Destination is not a directory.'
  [ -z "$(find "$destination" -mindepth 1 -printf x -quit)" ] ||
    fail 'Destination contains data; aborting without merging or overwriting it.'
fi
install -d -o tailhop -g tailhop -m 0700 "$destination"
cp -a --no-clobber -- "$source_state/." "$destination/"
chown -R tailhop:tailhop "$destination"
find "$destination" -type d -exec chmod 0700 {} +
find "$destination" -type f -exec chmod 0600 {} +
MIGRATE
```

**Continue only if the entire block succeeds.** If it fails, do not start the
service with a partial copy; investigate without deleting either identity. If
`/var/lib/tailhop` already contains data, establish which node it belongs to and
back it up separately before any manual resolution. These commands deliberately
abort rather than merge identities. They assume ordinary local Linux directories
and no concurrent writers. If the source is already `/var/lib/tailhop`, no copy
is needed; arrange a separate private backup and verify service ownership before
reusing it. Retain the original private state as a credential-bearing rollback
backup until migration is verified. Never move it or symlink the service path
into your home: `ProtectHome=true` blocks home access, while `StateDirectory`
keeps `/var/lib/tailhop` writable under `ProtectSystem=strict`.

If the old invocation used a custom `--hostname` or `TS_HOSTNAME`, preserve that
value as `TS_HOSTNAME` in `/etc/tailhop/tailhop.env`; also transfer any port or
other non-default settings to their environment equivalents. Use the root-owned
0600 file described above, with literal `KEY=value` assignments. The file may
remain absent when defaults suffice and valid enrolled state is present. A new
login or key may be needed if state is missing, invalid, revoked, or requires
reauthentication; generating a new key is not part of normal migration.

```bash
sudo systemctl enable --now tailhop
systemctl status tailhop
sudo journalctl -u tailhop -n 50 --no-pager
```

Confirm the **same device identity and Tailnet IP** in the Tailscale admin console,
not merely a matching hostname or active service status. From an authorized
Tailnet client, substitute the preserved DNS name/IP and configured port:

```sh
curl --noproxy "" --proxy http://tailhop:8080 http://example.com/
curl --noproxy "" --proxy http://tailhop:8080 https://ifconfig.me/ip
```

Keep journal output private; an enrollment URL can be a secret. An unexpected new
node or login prompt means migration needs investigation before retiring backups.

To roll back, stop and disable the service to prevent a later boot from starting
a duplicate. Verify it is inactive, then restart directly as the original user
with the preserved source path and original hostname/other flags:

```bash
sudo systemctl disable --now tailhop
# In the original user's shell, with source_state still set to the source path:
if [ "$(systemctl show -p ActiveState --value tailhop)" = inactive ]; then
  unset TS_AUTHKEY
  ./tailhop --state-dir "${source_state:?Set the original state path first}"  # Add original flags.
else
  printf '%s\n' 'Service has not stopped; do not start another instance.' >&2
fi
```

Do not run the service and direct process simultaneously with this identity.

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
with the same state path and flags.

To uninstall the systemd service and binary:

```sh
sudo systemctl disable --now tailhop
sudo rm -f /etc/systemd/system/tailhop.service /usr/local/bin/tailhop
sudo systemctl daemon-reload
```

These commands intentionally retain configuration, state, and the service
account. Removing state can require enrollment again and create a new identity.
