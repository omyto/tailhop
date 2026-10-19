# Docker deployment

The supplied [Compose file](../compose.yaml) builds the image from this checkout using the [Dockerfile](../Dockerfile). These instructions do not require a published registry image. Run commands from the repository root on a host with Docker and Compose, with outbound connectivity for Tailscale and proxied destinations.

## Configure and enroll

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
