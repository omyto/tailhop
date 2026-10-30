# Install tailhop on Linux

Use the archive for your Linux machine: `uname -m` reports `x86_64` for amd64 or
`aarch64` for arm64. You need CA certificates and outbound internet connectivity.
No Go, Git, Docker, TUN device, or separate tailscaled process is required.

## Install with one command

On Linux with systemd, download, verify, install, and start the latest public
stable release:

```sh
curl -fsSL https://raw.githubusercontent.com/omyto/tailhop/main/install.sh | sh
```

After startup, the installer checks logs from this service start for up to
30 seconds and prints the login URL as soon as it appears. Open it in your
browser and keep it private. If Tailscale reports Running, the installer reports
that no browser login is needed. If the URL is delayed or journal access is
unavailable, installation still completes and prints the command to follow logs:
`sudo journalctl -u tailhop -f`. `--no-start` skips this check.

This path requires curl, CA certificates, `awk`, GNU `tar`, `gzip`, `sha256sum`, and standard file
utilities (`sort`, `cmp`, `mkdir`, `mktemp`, `rm`), plus the systemd/account tools
described below. No Python is needed. Downloads run as your current user; the
installer uses sudo for the local installation when you are not root. It does not install missing dependencies.
Only amd64 (`x86_64`) and arm64 (`aarch64`) are supported.

The script selects the archive from GitHub's release API, checks its SHA-256
against the asset's GitHub digest **before extracting or executing anything in
it**, and invokes the installer bundled in that verified archive. Missing or
mismatched digests, unexpected archive contents, and download errors stop the
installation. A public repository and a published stable release containing the
installer are required; draft releases and prereleases are rejected. GitHub API
rate limits can cause a download to fail. The command trusts the installer from
`main`, GitHub release metadata, and the release publisher.

To pin a version, replace this example with an existing stable release tag:

```sh
curl -fsSL https://raw.githubusercontent.com/omyto/tailhop/main/install.sh | sh -s -- --version v0.2.0
```

Use `--no-start` to install without starting, stopping, or enabling the service:

```sh
curl -fsSL https://raw.githubusercontent.com/omyto/tailhop/main/install.sh | sh -s -- --no-start
```

Use this before configuring enrollment or migrating a direct-execution identity.
When running a saved script, add `--download` to use the online path:
`sh ./install.sh --download`. `--version` also selects the download path.
With no download option, a script run from a file uses its adjacent payload.
Repeat the online command to upgrade; configuration, systemd drop-ins, and state
are preserved. The main service unit is replaced; by default the installer
enables and restarts the service. License materials are installed in
`/usr/local/share/doc/tailhop`. The private temporary download directory is
removed on success or failure.

For installation without additional downloads, use the verified archive and
local steps below.

## Extract and verify

Download the matching architecture archive from the official
[GitHub Releases page](https://github.com/omyto/tailhop/releases). Public downloads
require a public repository and a maintainer-published, verified release. Compare its hash
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

The archive includes `tailhop`, `install.sh`, `LICENSE`, `THIRD_PARTY_NOTICES`, this guide, and
both files under `packaging/systemd/`. Keep the licensing materials when
redistributing the executable; the notices retain upstream terms and identify
outstanding attribution checks. They do not replace Tailhop's own MIT license.

Choose direct execution or the systemd service below. Run commands from this
extracted directory. Only the systemd administration commands need sudo.

## Run directly as an unprivileged user

Inspect flags, then run with defaults or customize the hostname and port:

```bash
./tailhop --version
./tailhop --help
./tailhop
./tailhop --hostname tailhop-home --port 8080
```

`--version` prints `tailhop <version> (commit <commit>; <Go version>)` and exits
without reading configuration or starting tsnet. Official release builds embed
the validated Git tag and commit. Local builds use `dev` and Go's embedded VCS
revision when available (with `-dirty` for modified source), otherwise `unknown`.

The default state directory is `$XDG_STATE_HOME/tailhop` when `XDG_STATE_HOME`
is absolute, otherwise `$HOME/.local/state/tailhop`. Tailhop creates it privately
with mode 0700 and checks writability. Relative or invalid XDG paths fall back to
the home directory. The working directory never determines the default.

With no key, run `./tailhop`, open the login URL printed in its logs in a browser,
and complete Tailscale login. Confirm the node in the Tailscale device view and
approve it if your Tailnet policy requires that. Keep the login URL private.
Startup/enrollment messages and the Tailnet readiness event remain visible even
with `--log-level=warn` or `--log-level=error`; application logs use the selected level.

For automation, supply `TS_AUTHKEY` through your deployment environment.
After enrollment, restart without the key and keep the same state directory.
Environment values can be inspected by privileged processes or those with the
same UID. The optional `--auth-key` flag is also supported, but literal keys can
appear in shell history and process listings. For manual setup, browser login
avoids creating and removing a key.

Press Ctrl-C (SIGINT) to stop gracefully, or send `kill -TERM <pid>` from another
terminal. The proxy drains requests/tunnels for up to ten seconds, then closes
remaining connections. Restart with the same non-secret flags and state directory:

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
The optional `--auth-key` flag falls back to `TS_AUTHKEY`; `--auth-key=` overrides
that environment value with an empty key. Tailhop does not parse `.env` files.
Other flags are `--hostname`, `--port`, `--state-dir`, `--log-level`,
`--max-clients`, and `--idle-timeout`; see `./tailhop --help`
for defaults and accepted values. `-h` also displays help without starting tsnet.

**Upgrading an existing native installation:** the old default was
`/var/lib/tailhop`. Keep using it with `./tailhop --state-dir /var/lib/tailhop`
or `TS_STATE_DIR=/var/lib/tailhop ./tailhop` to preserve identity (the runtime
user still needs access). Tailhop does not discover or migrate old state.
Using a different directory may enroll a new node; it never deletes old state.
Docker and the packaged systemd service continue using `/var/lib/tailhop`.

## Install as a systemd service

Already running directly? Install with `--no-start`, then follow
[Migrate from direct execution to systemd](#migrate-from-direct-execution-to-systemd)
before starting the service to preserve that node identity.

From the verified, extracted archive, install and start with one command:

```sh
sudo sh ./install.sh
```

The installer waits up to 30 seconds for the login URL from this service start
and prints it directly in the terminal. Open it in your browser to enroll when
needed, and keep it private. An already authenticated node reports Running
instead. If no URL or Running status is observed, use the printed journal command
to check progress; installation still completes. No auth key or environment file
is required. The installer creates the dedicated non-login
`tailhop` account, installs the binary, unit, and license materials, reloads
systemd, enables startup at boot, and starts or restarts the service. It uses only local archive contents;
it does not download files or install dependencies. Run it on the target Linux
host with a running systemd system manager and the `getent`, `groupadd`, `useradd`,
and standard file utilities available. It rejects an incompatible existing
`tailhop` account/group instead of repurposing it.

To install without starting, stopping, or enabling the service:

```sh
sudo sh ./install.sh --no-start
```

Use this mode before configuring automated enrollment or migrating state from
direct execution. Existing `/etc/tailhop/tailhop.env`, systemd drop-ins, and
`/var/lib/tailhop` are preserved. The bundled main unit is replaced; keep local
unit customizations in drop-ins. A running process continues using its old binary
until restarted. Default installation also enables a previously disabled service.

At startup, `StateDirectory=tailhop` creates and owns `/var/lib/tailhop` as
`tailhop:tailhop` with mode 0700. State persists across restarts and binary updates.
The unit fixes `--state-dir=/var/lib/tailhop`, taking precedence over `TS_STATE_DIR`.
Do not delete or replace existing state; installation does not migrate it.
For direct execution users, follow [the migration guide](#migrate-from-direct-execution-to-systemd)
before starting the service.

### Start and enroll interactively

If you installed with `--no-start`, enable and start when ready:

```sh
sudo systemctl enable --now tailhop
systemctl status tailhop
sudo journalctl -u tailhop -f
```

For a new node, open the journal's login URL in a browser and finish Tailscale
login, including any required device approval. Protect that URL as a secret.
An already enrolled node reuses valid state in `/var/lib/tailhop` without a key.
The directory must remain readable and writable by the service user.
No environment file is required in either case.

### Optional initial enrollment and configuration

For persistent non-secret settings, use `sudo systemctl edit tailhop` to add a
drop-in. Clear the old `ExecStart` before replacing it, and retain the packaged
state path and security settings. For example:

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/tailhop --state-dir=/var/lib/tailhop --hostname=tailhop-home --max-clients=200
```

Then run `sudo systemctl daemon-reload` and `sudo systemctl restart tailhop`.
Do not put authentication keys in permanent `ExecStart` arguments.

The unit retains optional `EnvironmentFile=-/etc/tailhop/tailhop.env` for legacy
installations and automation. Only if needed, create the root-owned 0600 file
without overwriting existing configuration:

```sh
sudo install -d -o root -g root -m 0700 /etc/tailhop
sudo sh -c 'umask 077; cp --no-clobber "$1" "$2"' sh packaging/systemd/tailhop.env.example /etc/tailhop/tailhop.env
sudo chown root:root /etc/tailhop/tailhop.env
sudo chmod 0600 /etc/tailhop/tailhop.env
sudoedit /etc/tailhop/tailhop.env
```

Use literal systemd `KEY=value` syntax without `export` or shell/Compose expansion.
An optional `TS_AUTHKEY` can enroll a new node automatically. Remove that assignment
after enrollment and restart to clear the running environment; valid state normally
needs no further key. Non-secret environment overrides remain supported, but CLI
flags win, including the fixed `--state-dir=/var/lib/tailhop`. No files are loaded
by Tailhop itself, and existing configuration and state are never deleted automatically.

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
Prepare the dedicated account, binary, and unit with `sudo sh ./install.sh --no-start`
as described in [Install as a systemd service](#install-as-a-systemd-service).
**Do not run `enable --now` or start the service yet**. Skip enrollment-key setup
when reusing state. If an existing environment file contains `TS_AUTHKEY`, remove that
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
sudo sh ./install.sh
systemctl status tailhop
sudo journalctl -u tailhop -n 50 --no-pager
```

The installer atomically replaces each binary/unit file and reloads systemd,
then enables and restarts the service. Keep `/etc/tailhop/tailhop.env` and
`/var/lib/tailhop` intact. Repeat the curl check and confirm the same Tailscale
device identity. If installation fails, inspect the error before starting or
restarting; it does not automatically roll back replaced files.
For direct execution, stop the old process, replace its executable, and restart
with the same state path and flags.

To uninstall the systemd service and binary:

```sh
sudo systemctl disable --now tailhop
sudo rm -f /etc/systemd/system/tailhop.service /usr/local/bin/tailhop
sudo systemctl daemon-reload
```

These commands intentionally retain configuration, state, license materials,
and the service account. Removing state can require enrollment again and create a new identity.
