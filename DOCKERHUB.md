# Tailhop

A lightweight HTTP/HTTPS forward proxy powered by Tailscale `tsnet`.

Tailhop lets devices on your Tailnet access the internet through a remote host's
network connection, without using a full Tailscale exit node. No public proxy
port, TUN device, privileged container, or separate `tailscaled` daemon is needed.

## Docker Run

For Docker, prefer `TS_AUTHKEY` for initial enrollment. Create a Tailscale auth key
for your intended Tailnet and approval/tag policy. In Bash, enter the key at a
hidden prompt, then start Tailhop:

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

Confirm the node is connected in the Tailscale device view and approve it if
required by your policy. Test the proxy using the command under Usage below.
A running container or `proxy listening` log alone does not prove enrollment is
complete.

After successful enrollment, remove the key from the container by recreating it
with the **same volume**, without `-e TS_AUTHKEY`:

```bash
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

Keep any custom non-secret settings when recreating. Valid persisted state is
reused without a new key. Unsetting the shell variable or restarting the existing
container does not clear its environment.

## Docker Compose

Create a `compose.yaml` file:

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
    environment:
      TS_AUTHKEY: ${TS_AUTHKEY:-}
    volumes:
      - state:/var/lib/tailhop

volumes:
  state:
    name: tailhop-state
```

Enter the initial enrollment key and start Tailhop in Bash:

```bash
read -r -s -p 'Tailscale auth key: ' TS_AUTHKEY
printf '\n'
export TS_AUTHKEY
docker compose config --quiet
docker compose pull
docker compose up -d
unset TS_AUTHKEY
```

After confirming enrollment and proxy access, remove the service's `environment`
block and recreate the container to clear the key:

```bash
docker compose up -d --force-recreate
```

Compose can also read `TS_AUTHKEY` from a `.env` file. If using one, restrict its
permissions (`chmod 600 .env`), never commit it, and remove the key after enrollment.
Tailhop itself does not load `.env` files. Use `docker compose config --quiet` to
validate without printing resolved credentials.

The example uses the same `tailhop-state` volume as Docker Run. Stop and remove
the old container before switching methods. If migrating an existing Compose
installation, keep its original volume; changing the volume can create a new
node identity.

## Alternative: browser enrollment

To enroll without an auth key, use the keyless Docker Run command above, or omit
the Compose `environment` block and start with `docker compose up -d`. Get the
login URL with:

```bash
docker logs -f tailhop
# For Compose:
docker compose logs -f tailhop
```

Open the URL, sign in, and approve the node if required. Keep the URL private;
it remains in container logs. Ctrl-C stops following logs, not the container.
No additional login command inside the container is required.

## Usage

From an authorized Tailnet device:

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

**Credential handling:** The hidden prompt avoids placing the key value in shell
history. Docker administrators can still inspect container environment values.
Avoid literal auth keys in CLI arguments; they can also appear in history,
process listings, and Docker metadata. Remove enrollment keys after use and
protect browser login URLs.

## Documentation

For configuration, security, and full deployment instructions:

https://github.com/omyto/tailhop

**License:** MIT
