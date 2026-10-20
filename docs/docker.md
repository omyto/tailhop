# Docker deployment

Use the [source-build Compose file](../compose.yaml) to build locally, or the [release Compose example](../compose.release.yaml) to pull a versioned Docker Hub image after it is published. Both need Docker with Compose and outbound connectivity for Tailscale and proxied destinations.

The first intended release is `0.1.0`; this guide does not establish that an image is already available. The publisher must supply the actual Docker Hub namespace. The GitHub source repository remains private; public images do not require repository access to run.

## Deploy a prebuilt image

Obtain `compose.release.yaml` and `.env.example` from the checkout or the maintainer, and put them in your deployment directory. Users without private repository access need these files supplied separately. Run the following from that directory:

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env`: replace `TS_AUTHKEY` with your own Tailscale Auth Key for initial enrollment (not a Tailscale API access token). Add these deployment settings, replacing the namespace placeholder with the publisher's actual Docker Hub username and choosing a version confirmed on Docker Hub:

```dotenv
DOCKERHUB_USERNAME=replace-with-publishing-username
TAILHOP_VERSION=0.1.0
```

These two settings select the image; they are not application configuration. The release example requires both, with no fallback image or `latest` tag. It pulls `<dockerhub-username>/tailhop:0.1.0` when that version is selected:

```sh
docker compose -f compose.release.yaml config --quiet
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d
docker compose -f compose.release.yaml logs -f tailhop
```

Allow intended clients through Tailnet grants/ACLs on TCP 8080 (or your configured proxy port), check enrollment in the Tailscale device view, then run the [client smoke tests](#manual-verification). The release workflow targets `linux/amd64` and `linux/arm64`; successful builds alone do not prove runtime behavior on both architectures.

After enrollment, remove `TS_AUTHKEY` from `.env` and clear it from the running container:

```sh
docker compose -f compose.release.yaml up -d --force-recreate
```

To upgrade, change `TAILHOP_VERSION` in `.env` to another published version, then run:

```sh
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d
```

Keep the same Compose project/deployment directory and named state volume so the replacement container reuses the enrolled identity. Changing the project name can select a different volume. Do not use `down -v` during normal upgrades: deleting state may require enrollment again. See [state permissions](#non-root-runtime-and-persistent-state) below.

## Build from source

Run from the repository root:

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env` and replace the `TS_AUTHKEY` placeholder with an enrollment key appropriate for your Tailnet's device approval and tag policy. See the [environment example](../.env.example) and [configuration reference](configuration.md).

```sh
docker compose config --quiet
docker compose up --build -d
docker compose logs -f tailhop
```

Use `config --quiet` to validate without printing the resolved authentication key. Confirm the node appears connected in the Tailscale device view. Restrict Tailnet grants/ACLs to intended peers on TCP 8080 (or your configured proxy port).

After successful enrollment, remove `TS_AUTHKEY` from `.env` and recreate the container to clear the running environment:

```sh
docker compose up -d --force-recreate
```

Valid persisted state allows subsequent starts without a new key. An uninitialized node without a key may log an interactive enrollment URL; automated deployments should supply a key. Protect that URL, environment files, and state as described in [secrets and state](security.md#secrets-and-state).

## Non-root runtime and persistent state

The image runs as UID/GID **10001:10001** from its first application start. `/var/lib/tailhop` is pre-created with that ownership and mode 0700. Docker copies these permissions into a new, empty named volume; keep Docker's default volume copy-up enabled. Existing volumes must already have the correct ownership. The application checks state writability and fails early on incorrect permissions.

The named `state` volume retains the Tailscale node identity across container replacement and ordinary `docker compose down`. Do not use `docker compose down -v` unless you intend to delete that identity and potentially enroll again.

If replacing the named volume with a host bind mount, prepare the directory manually before starting, and mount it at `/var/lib/tailhop`:

```sh
sudo install -d -m 0700 -o 10001 -g 10001 ./state
```

The runtime includes CA certificates. No TUN device, NET_ADMIN, privileged mode, host networking, or published port is used. Compose drops all capabilities and enables no-new-privileges. Apply workload-appropriate CPU, memory, and process limits through a Compose override; the global connection limit is not a complete resource bound.

## Lifecycle and troubleshooting

The commands below use the source-build `compose.yaml`. For prebuilt deployments, add `-f compose.release.yaml` immediately after `docker compose` in every lifecycle command.

```sh
docker compose ps
docker compose logs -f tailhop
docker compose restart tailhop
docker compose down
```

After updating the checkout, rebuild and replace the container with `docker compose up --build -d`; retain the state volume. SIGTERM/SIGINT allow a ten-second proxy drain before remaining connections are closed. Compose allows twenty seconds for drain and tsnet cleanup before forced termination; see [shutdown behavior](configuration.md#shutdown).

For startup failures, check logs, state ownership, configuration, and outbound connectivity. For an unreachable proxy, check node enrollment/approval, Tailnet grants/ACLs, the configured port, and DNS. HTTP 403 can indicate a destination denied by policy. Avoid dumping the container environment or resolved Compose configuration into diagnostics because it may contain the enrollment key.

## Manual verification

Image construction and first-run volume permissions require a host with Docker daemon access:

```sh
docker compose config --quiet
docker build .
```

After starting the service, run these from an authorized Tailnet client. The default hostname requires working MagicDNS; otherwise use the actual node DNS name or Tailnet IP. Empty double quotes work in POSIX shells and Windows CMD and override a client's `NO_PROXY` setting:

```sh
curl --noproxy "" --proxy http://tailhop:8080 http://example.com/
curl --noproxy "" --proxy http://tailhop:8080 https://ifconfig.me/ip

# Expected: HTTP 403
curl --noproxy "" -i --proxy http://tailhop:8080 http://127.0.0.1/
curl --noproxy "" -i --proxy http://tailhop:8080 http://169.254.169.254/
curl --noproxy "" -i --proxy http://tailhop:8080 http://example.com:22/
```

Only the curl command lines above are intended for Windows CMD; setup commands use a POSIX shell. Compare the reported IP with the actual container egress path, usually Docker host/upstream NAT, and the client's direct public IP. VPNs, cloud networking, and routing policy can change the observed address.

Confirm ACLs deny unauthorized peers, restart without an enrollment key and check identity preservation, and use `docker compose down` to confirm clean termination. Local tests and configuration validation do not verify live enrollment, Docker permissions, or Tailnet access controls.

## Maintainer release checklist

1. Create a **public** Docker Hub repository named `tailhop` under the intended username. Keep the GitHub source repository private.
2. In GitHub repository Settings → Secrets and variables → Actions, set repository variable `DOCKERHUB_USERNAME` to that username and repository secret `DOCKERHUB_TOKEN` to a Docker Hub access token with the read/write access needed to push. Never commit the token or pass it as a build argument. No Tailscale Auth Key is needed by CI or image builds.
3. Enable GitHub Actions for the private repository and allow the pinned official GitHub/Docker actions. Review runner/billing availability, limit who may push version tags, and run the branch CI before release. Keep action SHA pins updated deliberately.
4. Commit and review all release changes, then push the intended commit using the normal repository review process. Only when ready to publish, create and push the first version tag manually:

   ```sh
   git tag -a v0.1.0 -m "Release v0.1.0"
   git push origin v0.1.0
   ```

   Do not move an already published tag to another commit. Use a new version for changes.
5. Watch the [publishing workflow](../.github/workflows/docker-publish.yml). It validates stable `vMAJOR.MINOR.PATCH` tags, rejects leading zeros/prereleases/build metadata, and runs formatting, vet, unit tests, and race tests before the publish job. Ordinary branch pushes and pull requests cannot publish. `v0.1.0` produces only `<dockerhub-username>/tailhop:0.1.0`, without `latest`, aliases, or an automatic GitHub Release.
6. Check the resulting Docker Hub tag and its `linux/amd64` and `linux/arm64` manifest entries. With the actual namespace/version set in `.env`, pull and start using the prebuilt commands above. On each target architecture, verify non-root startup, first-run state permissions, HTTP/HTTPS forwarding, ACL enforcement, identity after a keyless restart, and clean shutdown. Use the [manual checks](#manual-verification); multi-platform runtime validation remains manual.

The workflow sets only application version/license OCI labels. It uses a local checked-out build context, disables Git labels/information, provenance and SBOM attestations, and build-record artifact uploads to avoid publishing private repository/build metadata. See [Docker's attestation documentation](https://docs.docker.com/build/ci/github-actions/attestations/) for the default behavior. Review image configuration/history and manifest metadata before announcing the release. Private Actions logs remain subject to GitHub repository access controls.

The build context allows only the current Go packages, module files, and Dockerfile. Add new build packages/assets explicitly to `.dockerignore`; do not include environments, credentials, or tsnet state. Keeping the source repository private does not make the distributed binary private: anyone can pull and inspect the public image and binary, including Go dependency/build information.
