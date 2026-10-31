# Deployment details

The base `docker-compose.yml` is the standalone HTTPS deployment: it publishes
only the gateway on 80/443. Run `docker compose up -d` after bootstrap and `.env`.

Optional overrides remain in the source repository for advanced proxy integration;
the installer does not download them. Do not stack proxy modes. These overrides
require Compose 2.24.4+ so inherited public ports are replaced, never appended.

| Mode | Override | Host ports | Certificate owner |
| --- | --- | --- | --- |
| Standalone | none | 80, 443 | rdsh gateway |
| Existing proxy on host | `compose.proxy-host.yml` | configurable, loopback only | existing proxy |
| Existing proxy in Docker | `compose.proxy-network.yml` | none | existing proxy |

The public `PUBLIC_URL` is an HTTPS origin with root path, for example
`https://dsh.example.com/`. A URL path prefix is not supported. Internal gateway
HTTP in external mode does not enable plain HTTP access to the browser: the external
proxy still provides HTTPS, and authentication cookies remain Secure.

## Standalone HTTPS

Point the domain's A/AAAA records at the server. Any published AAAA record must be
reachable too. Make ports 80 and 443 available to this deployment and allow them
through the firewall. Configure `PUBLIC_URL` and `ACME_EMAIL`, then:

```sh
docker compose up -d
```

Only the gateway publishes ports. ACME state is stored in its separate named
volume. DSH cannot read that volume. This setup does not require an extra certbot
container or a Docker socket.

## Existing reverse proxy on the host

Set `GATEWAY_HTTP_PORT` to an available port and start:

```sh
docker compose -f docker-compose.yml -f compose.proxy-host.yml up -d
```

Point the existing proxy at `127.0.0.1:GATEWAY_HTTP_PORT`.
[`nginx-host.conf`](../examples/nginx-host.conf) shows the needed Host, WebSocket,
streaming and request body settings. Merge it into the existing nginx configuration,
replace the domain and certificate paths, and use the same upstream port as Compose.
The example belongs inside nginx's `http` context; it does not replace your whole
nginx installation.

The gateway's socket peer may be a Docker bridge address instead of 127.0.0.1.
Set `TRUSTED_PROXIES` only to the actual proxy-side source address/CIDR if you need
per-client login rate limits. Without it, the forwarded client IP is ignored and
clients behind the same proxy share a rate-limit bucket. Never use `0.0.0.0/0` or
`::/0` merely to make the forwarded IP work.

## Existing reverse proxy in Docker

Set `PROXY_NETWORK` to the existing network used by your proxy:

```sh
docker compose -f docker-compose.yml -f compose.proxy-network.yml up -d
```

The proxy connects to `http://rdsh-gateway:8080` on that network. Use
[`nginx-docker.conf`](../examples/nginx-docker.conf) as a configuration example.
Only the gateway joins this network; DSH stays on the deployment's runtime network.
No host HTTP port is published. Configure the trusted proxy source narrowly as
above. Other containers on this network can contact the login endpoint, but cannot
bypass gateway authentication.

## Storage and permissions

| Storage | Mounted into | Access |
| --- | --- | --- |
| `DSH_HOME_PATH` | DSH `/home/dsh` | read/write |
| `handoff` named volume | both `/run/rdsh` | DSH write, gateway read-only |
| `gateway_state` named volume | gateway `/var/lib/rdsh` | gateway only |
| `AUTH_PASSWORD_HASH` | gateway environment | bcrypt hash, never the original password |

The published images run as fixed UID/GID `1000:1000`. The Bash installer creates
a new deployment directory, `.env` with mode 0600 and a dedicated home with mode
0700. It uses a one-shot root process with only `CAP_CHOWN` added to the published
DSH image to assign that new HOME to 1000:1000; no persistent service is started. It never reads an existing
`.env`, prompts for a password or changes an existing deployment. The single master
archive supplies matching Compose and configuration files; only those two allowlisted
files are extracted. Download/extraction failure cleans the
staging directory and does not create the destination. A failure during final copy
may leave a partial new directory; inspect it before retrying with a fresh path.

For a custom home path, prepare it as 1000:1000 with mode 0700. Supporting another
runtime UID requires custom image builds and corresponding ownership of the home
and named volumes; changing only Compose's `user` is insufficient.

Do not mount a real login home, SSH directory, credential store, or Docker socket.
The DSH home is intentionally writable by the agent, including its own settings
and installed packages. A container rebuild does not repair modifications in that
home or guarantee that data written by a newer DSH release can be read by an older one.

## Network and resource boundaries

The containers run without root, Linux capabilities, privileged mode or host
namespaces, with read-only root filesystems and bounded temporary storage. CPU,
memory and PID limits apply to ordinary Docker Compose containers, not just Swarm.
DSH's first installation also runs inside these limits. `/tmp` is mounted noexec;
native module extraction uses `/home/dsh/.cache/native` instead. The dedicated HOME
remains executable because DSH installs tools and native modules there.

The runtime bridge permits outbound access because npm, model APIs and agent tools
need it. Docker bridge networking alone does **not** prevent a process from contacting
host services, a LAN or cloud metadata endpoints. If those destinations need to be
blocked, enforce outbound policy on the Docker host and verify it from the running
container. Rules differ between Linux Docker, Docker Desktop and OrbStack; no
universal firewall snippet is silently installed by this project. For a stronger
host isolation boundary, deploy on a dedicated virtual machine.

Host administrators can inspect containers and mounted files. User data remains
writable to DSH. Backup and restore are the owner's responsibility using external
tools and are not part of rdsh.

## Password rotation and restarts

```sh
docker compose exec gateway /usr/local/bin/rdsh-gateway hash-password
# Put the resulting hash in AUTH_PASSWORD_HASH in .env, in single quotes.
docker compose up -d --force-recreate gateway
```

For an offline gateway, use `docker compose run --rm --no-deps gateway
hash-password`. The subcommand runs before server configuration validation and
does not need a password hash, a public domain, DSH or published ports.

Recreating reloads the environment; `restart` does not reload `.env`. Gateway
sessions and their random signing key live in memory, so a gateway restart logs
out every client and closes their connections. Restarting DSH does not change the
gateway password; its native credential is exchanged automatically.

## Optional file-based credentials and existing installations

The gateway still accepts an explicit `AUTH_PASSWORD_HASH_FILE` instead of
`AUTH_PASSWORD_HASH`; setting both nonempty is an error. Mount that file read-only
into the gateway yourself and remove/empty the environment hash. The file must be
readable by the gateway UID and not readable by others (for example 0600 or 0640).

`SESSION_SECRET_FILE` remains supported when explicitly set, for compatibility.
Without it, 32 cryptographically random bytes are generated at each gateway start.
A persistent signing key does not preserve process-local sessions across restarts.

When migrating an older rdsh checkout, remove `compose.https.yml` from the command:
HTTPS is now the base default. Transfer the old hash into the new `.env` yourself,
or explicitly retain the old file mounts and credential variables in your Compose.
Preserve the existing HOME ownership and named volumes. For an external proxy, apply
the updated proxy override or remove base ports and set `TLS_MODE: external` by hand
**before** recreating containers. Do not run the installer over an existing checkout.
