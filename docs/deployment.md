# Deployment details

The base `docker-compose.yml` publishes no host ports. Add exactly one publication
override. Changing modes means replacing that override in the command, not stacking
all of them. All commands below run from the repository root.

| Mode | Override | Host ports | Certificate owner |
| --- | --- | --- | --- |
| Standalone | `compose.https.yml` | 80, 443 | rdsh gateway |
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
docker compose -f docker-compose.yml -f compose.https.yml up -d --build
```

Only the gateway publishes ports. ACME state is stored in its separate named
volume. DSH cannot read that volume. This setup does not require an extra certbot
container or a Docker socket.

## Existing reverse proxy on the host

Set `GATEWAY_HTTP_PORT` to an available port and start:

```sh
docker compose -f docker-compose.yml -f compose.proxy-host.yml up -d --build
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
docker compose -f docker-compose.yml -f compose.proxy-network.yml up -d --build
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
| `secrets/password_hash` | gateway `/run/secrets/password_hash` | read-only |
| `secrets/session_secret` | gateway `/run/secrets/session_secret` | read-only |

Both images are built for `RDSH_UID:RDSH_GID`, default 1000:1000. Set these values
before building to match the owner of the dedicated home. The setup helper never
recursively changes existing data ownership. It requires private directories
(mode 0700) and private secret files (0600). Existing directories with a different
owner must be corrected deliberately before setup. When changing UID/GID on an
existing installation, rebuild both images and adjust ownership of their volumes
explicitly; a rebuild alone does not change existing named volume metadata.

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
python3 scripts/setup.py --rotate-password
docker compose -f docker-compose.yml -f compose.proxy-host.yml up -d --force-recreate gateway
```

Use your selected override in the second command. Recreate, rather than only restart,
reloads the atomically replaced bind-mounted password hash. Gateway sessions live in
memory, so recreating/restarting the gateway logs out every client and closes their
connections. The stable password can be filled by the browser password manager.

Restarting DSH does not change the gateway password. The adapter reads the current
DSH process credential and performs the native session exchange automatically.
