# Tailhop

A lightweight HTTP/HTTPS forward proxy powered by Tailscale `tsnet`.

Tailhop lets devices on your Tailnet access the internet through a remote host's
network connection, without using a full Tailscale exit node. No public proxy
port, TUN device, privileged container, or separate `tailscaled` daemon is needed.

For manual setup, sign in through your browser using the URL in the logs.
For automation, use `TS_AUTHKEY`. Existing valid state needs no new login or key.

## Docker Run

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

Open the login URL in your browser, sign in, and approve the node if required.
Keep the URL private; it remains in container logs. Ctrl-C stops following logs,
not the container. No login command inside the container is needed.

## Docker Compose

Create `compose.yaml`:

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

Start Tailhop and get the login URL:

```bash
docker compose up -d
docker compose logs -f tailhop
```

Complete browser login as above. If valid state already exists, no login is needed.
The example uses the same volume as Docker Run. Stop and remove the old container
before switching methods. Keep the original volume when updating an existing setup.

## Automated enrollment

Create a Tailscale auth key for your intended Tailnet and approval/tag policy.
Supply `TS_AUTHKEY` through your deployment environment:

- **Docker Run:** add `-e TS_AUTHKEY` before the image name.
- **Compose:** add this under the `tailhop` service:

```yaml
    environment:
      TS_AUTHKEY: ${TS_AUTHKEY:?Set TS_AUTHKEY for enrollment}
```

Compose can also read the key from `.env`. Keep that file private and out of
version control. Tailhop itself does not read `.env` files.

After confirming enrollment and proxy access, remove the key from your deployment
configuration and recreate the container with the same volume. For Docker Run,
stop and remove the container, then run the command above without `-e TS_AUTHKEY`.
For Compose, remove the key mapping and run `docker compose up -d --force-recreate`.
A restart or `unset` alone does not clear an existing container's environment.

Docker administrators can inspect environment values. Avoid literal keys in CLI
arguments, which can also appear in shell history and process listings.
See the [Docker guide](https://github.com/omyto/tailhop/blob/main/docs/docker.md#automated-enrollment-with-ts_authkey)
for a Bash prompt example and detailed instructions.

## Usage

Confirm the node is connected in the Tailscale device view and approve it if
required. A running container or `proxy listening` log alone does not mean the
node is connected. From an authorized Tailnet device:

```bash
curl --proxy http://tailhop:8080 https://ifconfig.me/ip
```

Ensure Tailscale grants/ACLs allow TCP port `8080`. Use the node's Tailnet IP if
MagicDNS is unavailable. The response shows the actual host/container egress IP.
Only public destination IPs on ports 80 and 443 are supported.

## Persistence and updates

The state volume preserves the Tailscale identity across restarts and container
replacement. Keep it private and do not share it between running instances.
Deleting it, including with `docker compose down -v`, can require enrollment again.
Expired or revoked authentication may also require reauthentication.

For updates, pull and recreate the container with the same volume and settings,
without an enrollment key when state is still valid. For Compose:

```bash
docker compose pull
docker compose up -d --force-recreate
```

Use an explicit published version instead of `latest` for predictable deployments.

## Documentation

For configuration, security, and full deployment instructions:

https://github.com/omyto/tailhop

**License:** MIT
