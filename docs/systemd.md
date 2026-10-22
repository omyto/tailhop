# Native Linux with systemd

The [service unit](../packaging/systemd/tailhop.service) runs the same executable as [Docker](docker.md), on Linux hosts with systemd. Installation commands below are for the administrator to run manually.

## Obtain the executable

Use a prebuilt binary when a trusted build artifact is available, or build from source. The target server needs Linux with systemd and CA certificates. A prebuilt installation requires neither Go nor Docker.

### Prebuilt Linux binary

The binary workflow stores outputs as private GitHub Actions artifacts, not public release downloads. An authorized maintainer can rebuild the existing tag without changing it:

1. First commit and make [linux-binaries.yml](../.github/workflows/linux-binaries.yml) available on the repository's default branch. Manual execution requires the workflow on that branch.
2. Open **Actions → Linux binaries → Run workflow**. Select the default branch as the workflow source and enter `release_tag=v0.1.0`. The workflow checks out and verifies the tagged commit before testing/building; it does not package main as `0.1.0`. Do not repush, move, or recreate the tag.
3. After a successful run, download `tailhop-linux-binaries-0.1.0` from the run's artifact list and extract the downloaded ZIP. It contains:

   ```text
   tailhop_0.1.0_linux_amd64.tar.gz
   tailhop_0.1.0_linux_arm64.tar.gz
   checksums.txt
   ```

Artifacts request 30-day retention, subject to repository policy. They require authorized repository access and are temporary, not permanent public release downloads. A maintainer can provide the files to an intended installer through a trusted channel.

Each architecture archive contains only `tailhop` and `LICENSE`. The maintainer must also supply the matching tagged [service unit](../packaging/systemd/tailhop.service) and [environment example](../packaging/systemd/tailhop.env.example). Select the same release tag in GitHub's source browser and download those files; retain their `packaging/systemd/` paths in the installation directory. No repository clone is needed on the server.

On the target server, identify the architecture:

```sh
uname -m
```

`x86_64` maps to `linux_amd64`; `aarch64` maps to `linux_arm64`. From the directory containing the downloaded archives and checksums, select, verify, and extract:

```sh
version=0.1.0
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo 'Unsupported Linux architecture'; exit 1 ;;
esac
sha256sum --check checksums.txt &&
  tar -xzf "tailhop_${version}_linux_${arch}.tar.gz" tailhop LICENSE
```

Stop if checksum verification fails. The check verifies both archives, so keep both next to `checksums.txt`. Checksums detect corruption; obtaining the archive and checksums from a trusted maintainer is still necessary. Extraction preserves the executable bit.

Continue with [service installation](#install-the-service) from the directory containing `./tailhop` and `packaging/systemd/`.

### Build from source

From the repository root, build as your regular user using mise:

```sh
mise exec -- go build -trimpath -o tailhop ./cmd/tailhop
```

Continue with the same service installation below.

## Install the service

Run the administration commands below manually on the target Linux host. Both installation methods provide `./tailhop` and use the same packaging paths.

Create the dedicated system group and non-login user if absent. If either already exists, confirm it is a suitable dedicated service account before continuing; do not repurpose an unrelated account. These commands use the distribution's `nologin` executable:

```sh
getent group tailhop >/dev/null || sudo groupadd --system tailhop
getent passwd tailhop >/dev/null || sudo useradd --system --gid tailhop --home-dir /var/lib/tailhop --no-create-home --shell "$(command -v nologin)" tailhop
```

Install the binary, root-only configuration, and unit. On an existing installation, keep the configured environment file instead of overwriting it with the example:

```sh
sudo install -o root -g root -m 0755 ./tailhop /usr/local/bin/tailhop
sudo install -d -o root -g root -m 0700 /etc/tailhop
sudo install -o root -g root -m 0600 packaging/systemd/tailhop.env.example /etc/tailhop/tailhop.env
sudoedit /etc/tailhop/tailhop.env
sudo install -o root -g root -m 0644 packaging/systemd/tailhop.service /etc/systemd/system/tailhop.service
sudo systemctl daemon-reload
sudo systemctl enable --now tailhop
```

Before starting, replace the TS_AUTHKEY placeholder with an appropriate enrollment key, or remove its assignment if reusing valid enrolled state. The [environment example](../packaging/systemd/tailhop.env.example) uses systemd `EnvironmentFile` syntax: `KEY=value`, without `export` or shell/Compose variable expansion. The [configuration table](configuration.md) applies to both deployments. Keep TS_STATE_DIR at **/var/lib/tailhop**: the unit sets it, and it must not be overridden in the environment file.

systemd reads the root-owned 0600 environment file before switching to `User=tailhop` / `Group=tailhop`. Environment variables are not a secret store: root and other processes running as the service account can inspect its environment, and diagnostic tools may expose it. Keep the account dedicated, avoid dumping the environment, and remove TS_AUTHKEY with `sudoedit` after successful enrollment. Restart the service to remove the key from the running process's environment. Valid persisted state reuses the same node identity without a new key; protect any interactive enrollment URL in the journal as a secret too.

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

Prepare `./tailhop` before stopping the service. For a prebuilt update, obtain the new artifact and matching checksums, then repeat the verification/extraction steps above with the new version. Stop if verification fails. For a source update, build the desired checkout:

```sh
mise exec -- go build -trimpath -o tailhop ./cmd/tailhop
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

## Future public distribution

When the repository becomes public, maintainers may distribute these versioned archives and checksums through GitHub Releases. No GitHub Release publishing is implemented now. Cross-compilation and archive checks do not prove ARM64 runtime behavior; test enrollment, forwarding, persistence, and service lifecycle on both target architectures before distributing a build as verified.
