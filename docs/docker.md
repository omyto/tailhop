# Docker deployment

Use the official public [omyto/tailhop image](https://hub.docker.com/r/omyto/tailhop).
End users need Docker and outbound connectivity for Tailscale and proxied
destinations; no source checkout, Go toolchain, or deployment-file download is
required. Start with Docker Run, or use Compose for declarative deployment.

## Docker Run: quick deployment

```bash
docker run -d \
  --name tailhop \
  --restart unless-stopped \
  --stop-timeout 20 \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v tailhop-state:/var/lib/tailhop \
  omyto/tailhop:latest
```

### Interactive enrollment

```sh
docker logs -f tailhop
```

On first start without an auth key, tsnet initiates interactive login. Open the
logged URL in a browser, sign in to the intended Tailnet, and finish enrollment.
Confirm the node appears connected in the Tailscale admin console and approve it
if your device policy requires it. Ctrl-C exits log following without stopping
the container. Keep the login URL private.

After browser authentication completes, tsnet finishes enrollment in the
background; no shell or Tailscale CLI inside the container is required. Valid
persisted state reuses the identity on later starts without a new key. Missing,
revoked, or expired state may require reauthentication.

From an authorized Tailnet client:

```sh
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

`tailhop` is the default hostname. MagicDNS must resolve it; otherwise use the
node's Tailnet IP. Grants/ACLs must allow TCP 8080 (or your configured port).
The response shows the host/container's actual public egress IP, including any
NAT, VPN, or routing policy. Add `--noproxy ""` if `NO_PROXY` would bypass it.

### Automated enrollment and configuration

For automated first-time enrollment, supply a Tailscale Auth Key appropriate for
your device approval/tag policy, not an API access token. In Bash, use this
**instead of** the interactive start command for a new container:

```bash
read -r -s -p 'Tailscale Auth Key: ' TS_AUTHKEY
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

The prompt avoids literal keys in command history, and `-e TS_AUTHKEY` copies the
exported value without putting it in the command arguments. Users with Docker
daemon access can inspect environment variables stored in container metadata;
unsetting the shell variable does not clear the container's copy. After successful
enrollment, recreate the container **without** `-e TS_AUTHKEY`, keeping the same
volume, using the [update sequence](#updates). Restarting alone retains its environment.

Defaults need no additional variables. Add optional flags **before the image
name**, such as `-e TS_HOSTNAME=tailhop-home`, `-e PROXY_PORT=8080`,
`-e LOG_LEVEL=info`, `-e MAX_CLIENTS=200`, or `-e IDLE_TIMEOUT=15m`.
See the [configuration reference](configuration.md) for accepted values. The
image already sets `TS_STATE_DIR=/var/lib/tailhop`; keep the volume mounted there.
Tailhop does not automatically load `.env` files.

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

Repeat any custom environment flags before the image name; omit `TS_AUTHKEY` when
valid enrolled state exists. For versioned deployments, use the chosen version
in both the pull and run commands. Confirm the same Tailscale device identity
and Tailnet IP, then repeat the proxy check. Do not run two containers using the
same identity at once, even if you copy its state to another volume.

## Docker Compose: persistent deployment

Save this self-contained example as `compose.yaml` in a deployment directory.
It needs no `.env` file or repository checkout:

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

Complete the same browser enrollment described above. The explicit volume name
matches Docker Run's `tailhop-state`. To switch methods, stop and remove the old
container before starting the other; never share active node state concurrently.
For a separate node, choose a different volume name and hostname.

For predictable production deployments, replace `latest` in `image:` with an
explicit published version. Optional application overrides go under the service,
for example:

```yaml
    environment:
      TS_HOSTNAME: tailhop-home
      MAX_CLIENTS: "200"
      IDLE_TIMEOUT: 15m
```

For auth-key enrollment, add `TS_AUTHKEY: ${TS_AUTHKEY:?Export TS_AUTHKEY for initial enrollment}`
to that `environment:` mapping. Read and export the key with the silent Bash prompt
above, run `docker compose up -d`, then `unset TS_AUTHKEY`. After enrollment,
remove the `TS_AUTHKEY` entry from the Compose file and recreate the container:

```sh
docker compose up -d --force-recreate
```

Compose interpolation supplies process environment variables; it is not a Tailhop
configuration parser. Use `config --quiet` rather than printing resolved keys.
Docker daemon access can expose secrets in container metadata, just as with Docker Run.

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

For users already working with repository deployment files,
[compose.release.yaml](../compose.release.yaml) remains supported. It requires an
explicit `TAILHOP_VERSION`; the development [compose.yaml](../compose.yaml) builds
source instead. In a fresh repository deployment, copy `.env.example` to `.env`,
protect it with `chmod 600 .env`, and select a published version (or explicitly
`latest`). Remove the example auth-key placeholder for interactive enrollment,
or replace it securely for key enrollment. Do not overwrite existing configuration.

```sh
docker compose -f compose.release.yaml config --quiet
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d
```

Use `-f compose.release.yaml` consistently for logs, lifecycle commands, and
upgrades. After key enrollment, remove `TS_AUTHKEY` from `.env` and unset any shell
export, then run `docker compose -f compose.release.yaml up -d --force-recreate`.
For upgrades, change `TAILHOP_VERSION` if pinned, pull, and recreate. This file uses
a project-scoped `state` volume; keep the same project/deployment directory and
volume. It does not automatically reuse the Quick Start's `tailhop-state` volume.

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
from the self-contained deployment example above. It passes application variables
from the shell or optional Compose `.env` interpolation. For interactive login,
leave `TS_AUTHKEY` unset; do not copy an auth-key placeholder into the environment.
`TAILHOP_VERSION` is not used here. Remove a supplied key after enrollment and
recreate with `docker compose -f compose.yaml up -d --force-recreate`.
Keep the same project-scoped state volume across builds.

Run source tests with the toolchain required by `go.mod`:

```sh
mise exec -- go test ./...
mise exec -- go test -race ./...
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for formatting, vet, and test requirements.

## Maintainer release checklist

1. Use the official public Docker Hub repository `omyto/tailhop`. Keep the GitHub source repository private.
2. In GitHub repository Settings → Secrets and variables → Actions, set repository variable `DOCKERHUB_USERNAME` to `omyto` (publisher-side configuration, not an end-user `.env` setting) and repository secret `DOCKERHUB_TOKEN` to a Docker Hub access token with the read/write access needed to push. Never commit the token or pass it as a build argument. No Tailscale Auth Key is needed by CI or image builds.
3. Enable GitHub Actions for the private repository and allow the pinned official GitHub/Docker actions. Review runner/billing availability, limit who may push version tags, and run the branch CI before release. Keep action SHA pins updated deliberately.
4. Commit and review release changes through the normal repository process. Publish only a new stable version tag containing the intended changes. Never move, repush, or recreate an already published tag.
5. Watch the [publishing workflow](../.github/workflows/docker-publish.yml). It validates stable `vMAJOR.MINOR.PATCH` tags, rejects leading zeros/prereleases/build metadata, and runs formatting, vet, unit tests, and race tests before the publish job. Ordinary branch pushes and pull requests cannot publish. A Git tag such as `v0.1.0` publishes `omyto/tailhop:0.1.0`. The same multi-platform build/push also updates `omyto/tailhop:latest`, following the publication-order behavior described under [image tags](#image-tags). No automatic GitHub Release is created.
6. Check the resulting Docker Hub tag and its `linux/amd64` and `linux/arm64` manifest entries. With the published version set as `TAILHOP_VERSION` in `.env`, pull and start using the prebuilt commands above. On each target architecture, verify non-root startup, first-run state permissions, HTTP/HTTPS forwarding, ACL enforcement, identity after a keyless restart, and clean shutdown. Use the [manual checks](#manual-verification); multi-platform runtime validation remains manual.

The workflow sets application version/license OCI labels. It uses a local checked-out build context, disables Git labels/information, provenance and SBOM attestations, and build-record artifact uploads to avoid publishing private repository/build metadata. See [Docker's attestation documentation](https://docs.docker.com/build/ci/github-actions/attestations/) for the default behavior. Review image configuration/history and manifest metadata before announcing the release. Private Actions logs remain subject to GitHub repository access controls.

The build context allows only the current Go packages, module files, and Dockerfile. Add new build packages/assets explicitly to `.dockerignore`; do not include environments, credentials, or tsnet state. Keeping the source repository private does not make the distributed binary private: anyone can pull and inspect the public image and binary, including Go dependency/build information.
