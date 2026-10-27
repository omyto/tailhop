# Docker deployment

The recommended deployment uses the official public
[omyto/tailhop image on Docker Hub](https://hub.docker.com/r/omyto/tailhop)
and the [release Compose file](../compose.release.yaml). No private repository
clone or local build is required. You need Docker with Compose and outbound
connectivity for Tailscale and proxied destinations. Choose an explicitly
published version; the release Compose file has no default version or `latest` fallback.
The local [compose.yaml](../compose.yaml) is reserved for [source development](#development-build-from-source).

Compose uses `.env` for interpolation and passes environment variables to Tailhop.
Tailhop does not parse `.env` itself. Environment-only configuration remains
supported; the image and both Compose files set `TS_STATE_DIR=/var/lib/tailhop`,
so native XDG/home defaults do not change container identity or persistence.

## Deploy a prebuilt image

Obtain `compose.release.yaml` and `.env.example` from the maintainer and put them
in a new deployment directory. These files can be supplied separately; deployment
does not require access to the private source repository. Run from that directory:

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env`: set the blank `TAILHOP_VERSION` to a published tag selected from
[Docker Hub](https://hub.docker.com/r/omyto/tailhop), and replace the `TS_AUTHKEY`
placeholder with your own Tailscale enrollment Auth Key (not an API access token).
The publishing workflow uses version tags without the Git tag's leading `v`.
Do not leave `TAILHOP_VERSION` empty or assume a `latest` tag exists.

Compose pulls `omyto/tailhop:<selected-version>`; no Docker Hub username setting
is needed. `TAILHOP_VERSION` selects the image and is not an application setting.
Validate without printing the resolved authentication key, then pull and start:

```sh
docker compose -f compose.release.yaml config --quiet
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d
docker compose -f compose.release.yaml logs -f tailhop
```

Allow intended clients through Tailnet grants/ACLs on TCP 8080 (or your configured proxy port), check enrollment in the Tailscale device view, then run the [client smoke tests](#manual-verification). The release workflow targets `linux/amd64` and `linux/arm64`; successful builds alone do not prove runtime behavior on both architectures.

After enrollment, remove `TS_AUTHKEY` from `.env` and unset it in your shell if
exported (shell variables can override Compose `.env` values). Recreate the
container to clear its running environment:

```sh
docker compose -f compose.release.yaml up -d --force-recreate
```

To upgrade, change `TAILHOP_VERSION` in `.env` to another published version, then run:

```sh
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d --force-recreate
```

Keep the same Compose project/deployment directory and named state volume so the replacement container reuses the enrolled identity. Changing the project name can select a different volume. Do not use `down -v` during normal upgrades: deleting state may require enrollment again. See [state permissions](#non-root-runtime-and-persistent-state) below.

Valid persisted state allows subsequent starts without a new key. An uninitialized
node without a key may log an interactive enrollment URL; automated first-time
deployments should supply a key. Protect that URL, environment files, and state
as described in [secrets and state](security.md#secrets-and-state).

## Non-root runtime and persistent state

The image runs as UID/GID **10001:10001** from its first application start. `/var/lib/tailhop` is pre-created with that ownership and mode 0700. Docker copies these permissions into a new, empty named volume; keep Docker's default volume copy-up enabled. Existing volumes must already have the correct ownership. The application checks state writability and fails early on incorrect permissions.

The named `state` volume retains the Tailscale node identity across container replacement and ordinary `docker compose down`. Do not use `docker compose down -v` unless you intend to delete that identity and potentially enroll again.

If replacing the named volume with a host bind mount, prepare the directory manually before starting, and mount it at `/var/lib/tailhop`:

```sh
sudo install -d -m 0700 -o 10001 -g 10001 ./state
```

The runtime includes CA certificates. No TUN device, NET_ADMIN, privileged mode, host networking, or published port is used. Compose drops all capabilities and enables no-new-privileges. Apply workload-appropriate CPU, memory, and process limits through a Compose override; the global connection limit is not a complete resource bound.

## Lifecycle and troubleshooting

Use `-f compose.release.yaml` consistently from the same deployment directory
so lifecycle commands target the image-based deployment:

```sh
docker compose -f compose.release.yaml ps
docker compose -f compose.release.yaml logs -f tailhop
docker compose -f compose.release.yaml restart tailhop
docker compose -f compose.release.yaml down
```

For image upgrades, change `TAILHOP_VERSION`, pull, and recreate as above; retain
the state volume. SIGTERM/SIGINT allow a ten-second proxy drain before remaining connections are closed. Compose allows twenty seconds for drain and tsnet cleanup before forced termination; see [shutdown behavior](configuration.md#shutdown).

For startup failures, check logs, state ownership, configuration, and outbound connectivity. For an unreachable proxy, check node enrollment/approval, Tailnet grants/ACLs, the configured port, and DNS. HTTP 403 can indicate a destination denied by policy. Avoid dumping the container environment or resolved Compose configuration into diagnostics because it may contain the enrollment key.

## Manual verification

Validate configuration, then pull the selected image on a host with Docker
daemon access:

```sh
docker compose -f compose.release.yaml config --quiet
docker compose -f compose.release.yaml pull
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

Confirm ACLs deny unauthorized peers, restart without an enrollment key and check identity preservation, and use `docker compose -f compose.release.yaml down` to confirm clean termination. Local tests and configuration validation do not verify live enrollment, Docker permissions, or Tailnet access controls.

## Development: build from source

For developers with source access, keep using the unchanged `compose.yaml` in a
repository checkout. This builds the local Dockerfile rather than pulling the
official release image. Run from the repository root, separately from your
prebuilt deployment directory:

```sh
cp .env.example .env
chmod 600 .env
```

Set `TS_AUTHKEY` for initial enrollment and adjust application settings as needed.
`TAILHOP_VERSION` is not used by this source-build Compose file.

```sh
docker compose -f compose.yaml config --quiet
docker compose -f compose.yaml up --build -d
docker compose -f compose.yaml logs -f tailhop
```

After enrollment, remove `TS_AUTHKEY` from `.env` and unset any shell export, then
run `docker compose -f compose.yaml up -d --force-recreate`. Rebuild after source
changes with `docker compose -f compose.yaml up --build -d`; retain the same state
volume. For image construction checks, run `docker build .` on a Docker host.
The enrollment, security, and persistence guidance above also applies here.

## Maintainer release checklist

1. Use the official public Docker Hub repository `omyto/tailhop`. Keep the GitHub source repository private.
2. In GitHub repository Settings → Secrets and variables → Actions, set repository variable `DOCKERHUB_USERNAME` to `omyto` (publisher-side configuration, not an end-user `.env` setting) and repository secret `DOCKERHUB_TOKEN` to a Docker Hub access token with the read/write access needed to push. Never commit the token or pass it as a build argument. No Tailscale Auth Key is needed by CI or image builds.
3. Enable GitHub Actions for the private repository and allow the pinned official GitHub/Docker actions. Review runner/billing availability, limit who may push version tags, and run the branch CI before release. Keep action SHA pins updated deliberately.
4. Commit and review release changes through the normal repository process. Publish only a new stable version tag containing the intended changes. Never move, repush, or recreate an already published tag.
5. Watch the [publishing workflow](../.github/workflows/docker-publish.yml). It validates stable `vMAJOR.MINOR.PATCH` tags, rejects leading zeros/prereleases/build metadata, and runs formatting, vet, unit tests, and race tests before the publish job. Ordinary branch pushes and pull requests cannot publish. A Git tag such as `v0.1.0` produces only `omyto/tailhop:0.1.0`, without `latest`, aliases, or an automatic GitHub Release.
6. Check the resulting Docker Hub tag and its `linux/amd64` and `linux/arm64` manifest entries. With the published version set as `TAILHOP_VERSION` in `.env`, pull and start using the prebuilt commands above. On each target architecture, verify non-root startup, first-run state permissions, HTTP/HTTPS forwarding, ACL enforcement, identity after a keyless restart, and clean shutdown. Use the [manual checks](#manual-verification); multi-platform runtime validation remains manual.

The workflow sets only application version/license OCI labels. It uses a local checked-out build context, disables Git labels/information, provenance and SBOM attestations, and build-record artifact uploads to avoid publishing private repository/build metadata. See [Docker's attestation documentation](https://docs.docker.com/build/ci/github-actions/attestations/) for the default behavior. Review image configuration/history and manifest metadata before announcing the release. Private Actions logs remain subject to GitHub repository access controls.

The build context allows only the current Go packages, module files, and Dockerfile. Add new build packages/assets explicitly to `.dockerignore`; do not include environments, credentials, or tsnet state. Keeping the source repository private does not make the distributed binary private: anyone can pull and inspect the public image and binary, including Go dependency/build information.
