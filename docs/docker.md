# Docker deployment

Use the official public [omyto/tailhop image](https://hub.docker.com/r/omyto/tailhop).
The repository overview text is maintained in [DOCKERHUB.md](../DOCKERHUB.md).
End users need Docker and outbound connectivity for Tailscale and proxied
destinations; no source checkout, Go toolchain, or deployment-file download is
required. Start with Docker Run, or use Compose for declarative deployment.

Choose enrollment by how you deploy:

- **Manual setup:** open the login URL from the logs. No auth key is needed.
- **Automation:** supply `TS_AUTHKEY` for initial enrollment.
- **Existing valid state:** keep the volume; no new login or key is needed.

## Docker Run: manual setup

```bash
docker run -d \
  --name tailhop \
  --restart unless-stopped \
  --stop-timeout 20 \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v tailhop-state:/var/lib/tailhop \
  omyto/tailhop:latest
docker logs -f tailhop
```

Open the login URL in your browser, sign in, and approve the node if your policy
requires it. Keep the URL private; it remains in container logs. Ctrl-C stops
following logs, not the container. No login command inside the container is needed.

Confirm the node is connected in the Tailscale device view. A running container
or `proxy listening` message alone does not prove enrollment is complete.
From an authorized Tailnet client, check forwarding:

```sh
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

`tailhop` is the default hostname. Use the node's Tailnet IP if MagicDNS is
unavailable. Grants/ACLs must allow TCP 8080 (or your configured port).
The response shows the host/container's public egress IP, including any NAT,
VPN, or routing policy. Add `--noproxy ""` if `NO_PROXY` would bypass the proxy.

### Automated enrollment with TS_AUTHKEY

Create a Tailscale auth key for your Tailnet's approval/tag policy, not an API
access token. Supply it through your deployment environment. To try this in Bash
without putting the key value in shell history:

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

Confirm connection, any required device approval, and proxy access as above.
Then stop and remove the container, and repeat the run command without
`-e TS_AUTHKEY`, using the **same volume**. Skip the browser login when state is
still valid. Repeat any custom non-secret settings too.

Unsetting the shell variable or restarting a container does not clear its saved
environment. Docker administrators can inspect environment values. Avoid literal
keys in CLI arguments, which can also appear in history and process listings.

Keep the state volume across restarts and updates. Missing, expired, or revoked
state may require another browser login or an auth key. Never run two instances
with the same state or copies of the same node identity at once.

### Optional configuration

Defaults need no configuration. Append CLI flags **after the image name**, for
example `--hostname=tailhop-home --max-clients=200 --idle-timeout=15m`.
The exec-form image ENTRYPOINT forwards these arguments directly to Tailhop.
CLI flags override non-empty environment variables, then defaults. Legacy variables
remain usable with `-e NAME=value` before the image name; no dotenv file is required.
See the [configuration reference](configuration.md) for all settings. The image
already sets `TS_STATE_DIR=/var/lib/tailhop`; keep the state volume mounted there.

### Container lifecycle

```sh
docker logs -f tailhop
docker stop tailhop
docker start tailhop
docker restart tailhop
```

To remove the container, stop it first:

```sh
docker stop tailhop
docker rm tailhop
```

These commands retain the named `tailhop-state` volume. Do not delete that volume
unintentionally: it contains the Tailscale node identity and credentials.
SIGTERM/SIGINT allow a ten-second proxy drain; `--stop-timeout 20` leaves time for
tsnet cleanup before forced termination.

### Updates

Updating an image tag on Docker Hub does not update running containers. Pull and
recreate, using the **same volume** and your original non-secret settings:

```bash
docker pull omyto/tailhop:latest
docker stop tailhop
docker rm tailhop
docker run -d \
  --name tailhop \
  --restart unless-stopped \
  --stop-timeout 20 \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v tailhop-state:/var/lib/tailhop \
  omyto/tailhop:latest
```

Repeat custom CLI flags after the image name (or legacy environment flags before
it). Omit `--auth-key` and `TS_AUTHKEY` when valid enrolled state exists. For versioned deployments, use the chosen version
in both the pull and run commands. Confirm the same Tailscale device identity
and Tailnet IP, then repeat the proxy check. Do not run two containers using the
same identity at once, even if you copy its state to another volume.

## Docker Compose: persistent deployment

Save this as `compose.yaml`. No `.env` file or auth key is needed for manual setup:

```yaml
services:
  tailhop:
    image: omyto/tailhop:latest
    restart: unless-stopped
    stop_grace_period: 20s
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    volumes:
      - state:/var/lib/tailhop
volumes:
  state:
    name: tailhop-state
```

```sh
docker compose config --quiet
docker compose pull
docker compose up -d
docker compose logs -f tailhop
```

For a new node, open the login URL and complete the same browser login described
above. Existing valid state is reused without another login.

The volume name matches Docker Run's `tailhop-state`. Stop and remove the old
container before switching methods. For an existing deployment, keep its original
volume. For a separate node, use a different volume name and hostname.

### Automated Compose enrollment

For automation, add this under the `tailhop` service before the first start:

```yaml
    environment:
      TS_AUTHKEY: ${TS_AUTHKEY:?Set TS_AUTHKEY for enrollment}
```

Supply `TS_AUTHKEY` from your deployment environment, then run
`docker compose up -d`. For a manual test, use the Bash key prompt shown above before running
Compose. Compose can also read the value from a `.env` file; keep that file
private (`chmod 600 .env`) and out of version control. Tailhop itself does not
read `.env` files.

After confirming enrollment and proxy access, remove the `TS_AUTHKEY` mapping
and the key from its source, then recreate the container:

```sh
unset TS_AUTHKEY
docker compose up -d --force-recreate
```

Keep the same volume. A restart alone does not clear the container's environment.
Use `docker compose config --quiet` to validate without printing the resolved key.

### Configuration and updates

For predictable production deployments, replace `latest` in `image:` with an
explicit published version. Optional customizations use service `command`:

```yaml
    command:
      - --hostname=tailhop-home
      - --max-clients=200
      - --idle-timeout=15m
```

The image ENTRYPOINT remains Tailhop. Do not put an enrollment key in permanent
Compose `command` arguments. Environment values are also visible to Docker
administrators; remove the key after enrollment as described above.

For upgrades, change the image version if pinned, then:

```sh
docker compose pull
docker compose up -d --force-recreate
```

Keep the same named volume to preserve identity. Ordinary `docker compose down`
retains it; **`docker compose down -v` can delete it and the Tailscale identity**.
Use `docker compose ps`, `logs -f tailhop`, `stop`, and `restart tailhop` to operate
the service.

### Repository release Compose file

The repository's [compose.release.yaml](../compose.release.yaml) also defaults to
`omyto/tailhop:latest`, with no `.env`, auth key, or version variable required:

```sh
docker compose -f compose.release.yaml config --quiet
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d
```

Use `-f compose.release.yaml` consistently for lifecycle commands and upgrades.
Pin a version by editing `image:`; add `command` flags for optional settings.
For initial key enrollment, add the explicit `environment` mapping from
[Automated Compose enrollment](#automated-compose-enrollment) and remove the key
after enrollment. Without that mapping, use browser login for a new node.
This file retains its original project-scoped `state` volume: keep the same
project/deployment directory and volume across updates. It does not automatically
reuse the Quick Start's `tailhop-state` volume or migrate existing state.

Previous `.env` users must transfer needed customizations to CLI `command` or an
explicit `environment` mapping: Compose does not forward host variables to a
container automatically. Environment-based application configuration still works;
only the default interpolation has been removed. Existing user `.env` files are
not deleted automatically, but are no longer needed or referenced by these files.

## Image tags

`omyto/tailhop:latest` is convenient for quick installation and testing. Prefer
`omyto/tailhop:<version>` for predictable production deployments. For example:

```sh
docker pull omyto/tailhop:latest
docker pull omyto/tailhop:0.2.0
```

`0.2.0` illustrates a versioned tag; choose a version published on Docker Hub.
Each stable release publishes its versioned tag and `latest` from the same
multi-platform build (`linux/amd64`, `linux/arm64`). `latest` follows publication
order, not the highest SemVer version; rerunning an older stable release may
overwrite it. Prerelease tags are rejected. Pulling and recreating with the same
state volume is required to update containers and retain identity.

## Non-root runtime and persistent state

The image runs as UID/GID **10001:10001**. `/var/lib/tailhop` is pre-created with
that ownership and mode 0700. Docker copies these permissions into a new, empty
named volume; keep default volume copy-up enabled. Existing volumes must already
be writable by that UID. Tailhop secures the directory to 0700 and checks writability.
Protect state and backups as credentials; never delete them during normal updates.

For a host bind mount instead, prepare it before starting and mount it at
`/var/lib/tailhop`:

```sh
sudo install -d -m 0700 -o 10001 -g 10001 ./state
```

CA certificates are included. No public proxy port, TUN device, NET_ADMIN,
privileged mode, host networking, or separate tailscaled daemon is needed.
Both methods drop all capabilities and enable no-new-privileges. Apply CPU,
memory, and process limits appropriate to your workload; the global connection
limit is not a complete resource bound.

## Manual verification

For startup failures, check logs, outbound connectivity, configuration, and state
ownership. For an unreachable proxy, check enrollment/approval, Tailnet grants/ACLs,
port, and DNS. "Proxy listening" or an active container alone does not prove that
enrollment is complete. Keep enrollment URLs and authentication material private.

From an authorized Tailnet client, test forwarding and destination restrictions:

```sh
curl --noproxy "" --proxy http://tailhop:8080 http://example.com/
curl --noproxy "" --proxy http://tailhop:8080 https://ifconfig.me/ip

# Expected: HTTP 403
curl --noproxy "" -i --proxy http://tailhop:8080 http://127.0.0.1/
curl --noproxy "" -i --proxy http://tailhop:8080 http://169.254.169.254/
curl --noproxy "" -i --proxy http://tailhop:8080 http://example.com:22/
```

Confirm ACLs deny unauthorized peers, restart without an auth key and verify the
same identity, then check graceful shutdown. See [security](security.md) for
boundaries. Local configuration checks do not verify live enrollment, runtime
permissions, ACL enforcement, or architecture-specific behavior.

## Development: build from source

End users do not need a source checkout or these build steps. For developers,
run from the repository root:

```sh
docker build -t tailhop-dev .
docker compose -f compose.yaml config --quiet
docker compose -f compose.yaml up --build -d
docker compose -f compose.yaml logs -f tailhop
```

The repository's development `compose.yaml` builds the Dockerfile; it is distinct
from the self-contained deployment example above. It needs no `.env` and uses
interactive enrollment by default. Add service `command` flags for optional settings,
or an explicit `environment` mapping for automation. Remove any supplied key after
enrollment and recreate with `docker compose -f compose.yaml up -d --force-recreate`.
Keep the same project-scoped state volume across builds.

Run source tests with the toolchain required by `go.mod`:

```sh
go test ./...
go test -race ./...
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for formatting, vet, and test requirements.

## Maintainer release checklist

1. Use the official public Docker Hub repository `omyto/tailhop` and the [source repository](https://github.com/omyto/tailhop).
2. In GitHub repository Settings → Secrets and variables → Actions, set repository variable `DOCKERHUB_USERNAME` to `omyto` (publisher-side configuration, not an end-user `.env` setting) and repository secret `DOCKERHUB_TOKEN` to a Docker Hub access token with the read/write access needed to push. Never commit the token or pass it as a build argument. No Tailscale Auth Key is needed by CI or image builds.
3. Enable GitHub Actions for the repository and allow the pinned official GitHub/Docker actions. Review runner/billing availability, limit who may push version tags, and run the branch CI before release. Keep action SHA pins updated deliberately.
4. Commit and review release changes through the normal repository process. Publish only a new stable version tag containing the intended changes. Never move, repush, or recreate an already published tag.
5. Watch the [publishing workflow](../.github/workflows/docker-publish.yml). It validates stable `vMAJOR.MINOR.PATCH` tags, rejects leading zeros/prereleases/build metadata, and runs formatting, vet, unit tests, and race tests. It then calls the shared CI workflow with that verified commit; fuzzing and both Linux amd64/arm64 vulnerability scans must pass before the publish job can access Docker Hub credentials or push an image. A failed, canceled or skipped gate blocks publication; missing fuzz targets also fail the gate. Ordinary branch pushes and pull requests cannot publish. A Git tag such as `v0.1.0` publishes `omyto/tailhop:0.1.0`. The same multi-platform build/push also updates `omyto/tailhop:latest`, following the publication-order behavior described under [image tags](#image-tags). No automatic GitHub Release is created.
6. Check the resulting Docker Hub tag and its `linux/amd64` and `linux/arm64` manifest entries. Select the published version in the image reference, then pull and start using the prebuilt commands above. On each target architecture, verify non-root startup, first-run state permissions, HTTP/HTTPS forwarding, ACL enforcement, identity after a keyless restart, and clean shutdown. Use the [manual checks](#manual-verification); multi-platform runtime validation remains manual.

The workflow sets application version/license OCI labels. It uses a local checked-out build context, disables Git labels/information, provenance and SBOM attestations, and build-record artifact uploads to avoid publishing unintended source/build metadata. See [Docker's attestation documentation](https://docs.docker.com/build/ci/github-actions/attestations/) for the default behavior. Review image configuration/history and manifest metadata before announcing the release. Review Actions logs for secrets before making repository history public.

The build context allows only the current Go packages, module files, Dockerfile, and required license materials. Add new build packages/assets explicitly to `.dockerignore`; do not include environments, credentials, or tsnet state. Anyone can inspect public images and binaries, including Go dependency/build information. The Dockerfile copies Tailhop license materials to `/usr/share/doc/tailhop/`; verify those and the separate OS package notices in released images.
