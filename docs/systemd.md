# Native Linux with systemd

The [service unit](../packaging/systemd/tailhop.service) runs the same executable as [Docker](docker.md), on Linux hosts with systemd. Installation commands below are for the administrator to run manually.

## Obtain the executable

Obtain prebuilt Linux archives from [GitHub Releases](https://github.com/omyto/tailhop/releases).
Start with the bundled [INSTALL.md](../INSTALL.md): it covers direct execution and a complete systemd installation using only archive contents. Alternatively, build from source below. The target server needs Linux with systemd and CA certificates. A prebuilt installation requires neither Go nor Docker.

### Prebuilt Linux binary

The binary workflow creates or updates a **GitHub Draft Release** after Go checks, package validation, and archive integrity verification succeed. It never publishes a release. Draft assets are not public downloads; a maintainer must review and publish a verified draft. An authorized maintainer can rebuild the existing tag without changing it:

1. Make [linux-binaries.yml](../.github/workflows/linux-binaries.yml) available on the default branch for manual execution. Choose an existing stable release tag containing `INSTALL.md`, `LICENSE`, `THIRD_PARTY_NOTICES`, and both systemd packaging files. Everything bundled must come from that tagged source.
2. Open **Actions → Linux binaries → Run workflow**. Select the default branch as the workflow source and enter the chosen tag in `release_tag`. The workflow checks out and verifies that exact tagged commit before testing/building. Do not repush, move, or recreate tags.
3. After both jobs succeed, open **Releases** and select the draft for that tag. Download only the archive matching the target machine: `tailhop_<version>_linux_amd64.tar.gz` for `x86_64`, or `tailhop_<version>_linux_arm64.tar.gz` for `aarch64` (`uname -m` identifies it).

The existing `v0.1.0` tag predates the installation guide and third-party notices. The workflow rejects tags missing required materials rather than mixing old binaries with newer files. A maintainer must later choose a new version tag containing all required files; no existing tag should be moved.

Draft release assets are separate from the temporary Actions artifact used between jobs. That artifact still requests 30-day retention, subject to repository policy; its expiration does not remove the draft assets. Drafts require authorized access and may be deleted by maintainers. After release review, publish the draft to make its archives available through the official Releases page. There are no checksum manifest files inside archives or as release assets. The build job passes each verified archive’s SHA-256 through workflow outputs; the release job verifies downloaded artifact bytes against those hashes before any release changes, and checks uploads against GitHub asset digests.

The build job retains read-only repository permissions. Only the release job uses the built-in `GITHUB_TOKEN` with `contents: write`; repository policies must permit that access. No Docker Hub or Tailscale credentials are needed by the workflow.

Reruns reuse the existing draft, leave identical assets alone, and stage changed uploads under temporary names. All replacements are checked against GitHub's SHA-256 digests before old assets are renamed to backups. Backups are deleted only after the complete new set is verified; unrelated attachments are preserved. The job refuses to modify a published release.

Failed uploads or asset operations fail the job. Interrupted updates can leave `tailhop-staging-...` or `tailhop-backup-...` attachments; inspect the failure and rerun to recover, rather than deleting the release or using `gh release upload --clobber`. The updater attempts rollback after rename failures and retains bytes when recovery is incomplete. Updates for the same tag are serialized, but GitHub's API operations are not transactional: do not publish or manually edit the draft or tag while the job is running. Confirm a successful run and both final archive filenames before distributing files. A recognized legacy `checksums.txt` attachment is removed only after both replacement archives verify; an unrecognized checksum attachment is retained for manual review, as are unrelated assets.

Each archive includes:

```text
tailhop
LICENSE
THIRD_PARTY_NOTICES
INSTALL.md
packaging/systemd/tailhop.service
packaging/systemd/tailhop.env.example
```

Before extracting, run `sha256sum` on the downloaded archive and compare the result with that asset’s SHA-256 digest on GitHub. Stop if it differs. Then extract into a fresh directory:

```sh
printf 'Path to the downloaded Linux archive: '
IFS= read -r archive
mkdir tailhop-install
tar -xzf "$archive" -C tailhop-install
cd tailhop-install
```

Follow `INSTALL.md` in that directory. It needs no repository clone or extra configuration downloads. Obtain the archive and GitHub digest through trusted access; digest matching verifies bytes, not software safety.

### Build from source

From the repository root, build as your regular user with Go 1.27.3 or newer:

```sh
go build -trimpath -o tailhop ./cmd/tailhop
```

Continue with the same service installation below.

Already running the binary directly? Follow [Migrate from direct execution to systemd](../INSTALL.md#migrate-from-direct-execution-to-systemd) to preserve the existing node identity without a new enrollment key.

## Install the service

Run the administration commands below manually on the target Linux host. Both installation methods provide `./tailhop` and use the same packaging paths.

Create the dedicated system group and non-login user if absent. If either already exists, confirm it is a suitable dedicated service account before continuing; do not repurpose an unrelated account. These commands use the distribution's `nologin` executable:

```sh
getent group tailhop >/dev/null || sudo groupadd --system tailhop
getent passwd tailhop >/dev/null || sudo useradd --system --gid tailhop --home-dir /var/lib/tailhop --no-create-home --shell "$(command -v nologin)" tailhop
```

Install the binary and unit; no environment file is needed for installation:

```sh
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo install -o root -g root -m 0644 packaging/systemd/tailhop.service /etc/systemd/system/tailhop.service
sudo systemctl daemon-reload
```

### Start and enroll

```sh
sudo systemctl enable --now tailhop
systemctl status tailhop
sudo journalctl -u tailhop -f
```

For a new node, open the logged URL in a browser, complete login, and approve the
node if required by Tailnet policy. tsnet supports this interactive flow without
a key. Protect the URL as a secret. Existing valid state is reused without a new
key, provided the service user can read and write `/var/lib/tailhop`.
No environment file is needed for normal startup or enrollment.

### Optional configuration and automation

Use a `systemctl edit tailhop` drop-in for persistent non-secret CLI flags; see the
self-contained [configuration example](../INSTALL.md#optional-initial-enrollment-and-configuration).
Keep `--state-dir=/var/lib/tailhop` and the packaged hardening. Never place a key
in permanent `ExecStart` arguments. For direct execution, the optional `--auth-key`
flag is available, with the [documented exposure risks](configuration.md).

The unit retains `EnvironmentFile=-/etc/tailhop/tailhop.env` for backward
compatibility and automation. Only create it when needed, following the same
[optional setup](../INSTALL.md#optional-initial-enrollment-and-configuration), which
preserves existing files. Use root:root, mode 0600, in `/etc/tailhop` (0700).
Use literal `KEY=value` assignments without `export` or shell expansion.
`TS_HOSTNAME`, `PROXY_PORT`, `LOG_LEVEL`, `MAX_CLIENTS`, `IDLE_TIMEOUT`, and optional
`TS_AUTHKEY` remain supported; CLI flags win, including the unit's state path.
After key enrollment remove `TS_AUTHKEY` and restart to clear the environment.
Environment variables are inspectable by root and processes using the service UID.
Tailhop itself loads no configuration files. No state is migrated or deleted.

`StateDirectory=tailhop` creates and owns /var/lib/tailhop for the service, with mode 0700. It persists across stops, restarts, and binary updates. Replacing the executable must not delete or replace this Tailscale identity. Switching from Docker to systemd does not automatically migrate the container’s tsnet state. Do not put state in /tmp. The unit runs without root, TUN, capabilities, a separate tailscaled daemon, firewall changes, or a public proxy listener. NoNewPrivileges, a private umask, read-only system paths and inaccessible home directories restrict writes; a private writable temporary directory supports temporary files while persistent state remains writable through StateDirectory.

The service uses Type=simple: active status means the process started, not that Tailnet enrollment is complete. Check the Tailscale device view and use the [client verification](#verify-and-operate) below. It starts after network-online.target, retries unexpected failures after five seconds, and receives SIGTERM on stop. TimeoutStopSec=20s allows the existing ten-second proxy drain plus tsnet cleanup before systemd force-kills a stuck process. Normal stdout/stderr go to journald.

## Verify and operate

From an authorized Tailnet client, test HTTPS forwarding:

```sh
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

The hostname requires working Tailnet DNS; otherwise use the node’s Tailnet IP. Inbound traffic arrives through tsnet, while outbound traffic uses the host’s normal route. No public port is needed. Restrict access with Tailnet grants/ACLs. See [security](security.md) for boundaries and limitations.

Service operations:

```sh
systemctl status tailhop
sudo journalctl -u tailhop -f
sudo systemctl restart tailhop
sudo systemctl stop tailhop
```

## Update

Prepare `./tailhop` before stopping the service. For a prebuilt update, obtain the new matching architecture archive, then compare its SHA-256 with the GitHub asset digest before extracting, as described in the bundled `INSTALL.md`. Stop if verification fails. For a source update, build the desired checkout:

```sh
go build -trimpath -o tailhop ./cmd/tailhop
```

For either method, keep /etc/tailhop/tailhop.env and /var/lib/tailhop intact, stop the service, replace only the executable, and restart:

```sh
sudo systemctl stop tailhop
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo systemctl restart tailhop
systemctl status tailhop
```

Check logs with `sudo journalctl -u tailhop -n 50 --no-pager`, confirm the same device identity remains in the Tailscale device view, and repeat the client curl test. If updating the unit itself, install its new version and run `sudo systemctl daemon-reload` before restarting.

## Uninstall

To uninstall the executable and unit:

```sh
sudo systemctl disable --now tailhop
sudo rm -f /etc/systemd/system/tailhop.service /usr/local/bin/tailhop
sudo systemctl daemon-reload
```

These commands preserve /var/lib/tailhop, /etc/tailhop, and the service account. Removing persisted tsnet state may require enrollment again and create a new node identity. Native service packaging here supports Linux with systemd.

## Manual service verification

Validate the unit without installing or starting it:

```sh
systemd-analyze verify packaging/systemd/tailhop.service
```

A missing /usr/local/bin/tailhop before installation is an expected diagnostic; distinguish it from structural unit errors. Structural validation does not test runtime sandbox compatibility.

After manual installation, check startup, journal output, state ownership (tailhop:tailhop, mode 0700), successful enrollment, ACL enforcement, and graceful shutdown on the actual host. Restart without an enrollment key and confirm the same Tailscale device identity. Verify the observed public IP matches the host’s actual egress path; VPNs and routing policy may affect it.

## Public distribution

Maintainers may publish a verified draft for public binary distribution after verifying bundled license materials and completing runtime checks. Drafts remain non-public until a maintainer publishes them; this workflow never performs that step. Cross-compilation and archive checks do not prove ARM64 runtime behavior; test enrollment, forwarding, persistence, and service lifecycle on both target architectures before distributing a build as verified.
