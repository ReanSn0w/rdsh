# Runtime and persistent home

The container runs as the UID/GID selected at build time (`RDSH_UID=1000`, `RDSH_GID=1000` by default). Create a **dedicated** directory on the host and give that user ownership before starting Compose. Do not mount the server owner's actual home. Custom IDs require rebuilding the image with matching build arguments, not only changing Compose's `user` field.

The wrapper never recursively changes ownership or applies `chmod 777` to existing data. Missing, unwritable or world-writable homes are configuration errors. On Linux, an administrator can prepare the dedicated directory with `install -d -m 0700 -o UID -g GID /chosen/dsh-home`, substituting the chosen numeric IDs and path. Run that command only against the intended new directory.

| Container path | Purpose |
| --- | --- |
| `/home/dsh/.rdsh/releases` | Versioned DSH installations and their locks |
| `/home/dsh/.rdsh/runtime.lock` | Kernel-held lock preventing concurrent use of the same HOME |
| `/home/dsh/.dsh` | DSH settings, sessions and credentials owned by DSH |
| `/home/dsh/.cache/npm` | npm download cache |
| `/home/dsh/.cache/native` | Native addon cache; requires executable filesystem |
| `/home/dsh/workspaces` | Default working directory for user projects |
| `/run/rdsh/connection.json` | Separate transient handoff to the gateway; not part of HOME |

All required persistent state is beneath the bind-mounted home. Recreating the container retains this state; DSH itself can modify the files it owns. Backup and restore procedures are outside this project's scope.

## Installation and updates

Only versions present under the image's `releases/` directory are accepted. Each release has a reviewed `package.json` and `package-lock.json`; runtime installation uses `npm ci --ignore-scripts`. The one explicitly invoked pinned helper restores node-pty's spawn-helper executable bit. Arbitrary dependency lifecycle scripts are not enabled.

An installation is first built in a new staging directory, then marked ready and atomically published. A failed installation never becomes active and does not delete DSH settings or projects. A kernel `flock` covers the complete runtime lifecycle to prevent two containers from sharing the same HOME concurrently (exit 75 on contention). Do not run two DSH instances against one home.

Subsequent starts run a local `--version` executable check and reuse the validated version without reinstalling from npm. A broken executable triggers a fresh locked installation. The current-installation pointer is refreshed on reuse as well as installation. To update, obtain a wrapper image containing the new release lock, explicitly change DSH_VERSION, and recreate the container. Changing DSH_VERSION to an arbitrary npm version is rejected. Older installation directories remain present, but upstream data-format migrations can make downgrades incompatible; no automatic data rollback is provided.

The active installation's npm executable directory is on the image `PATH` through the atomic `~/.rdsh/current` link. After the container becomes healthy, administrative commands therefore work without knowing the versioned installation path, for example `docker compose exec dsh dsh --version`. The same path follows an activated DSH update automatically. The image also activates the upstream release's pinned `pnpm@11.28.5`, which `dsh plugin` needs to manage external packages; Corepack is configured not to select a floating latest version.

Installation has bounded npm network retries and a ten-minute process timeout. Failed first install exits nonzero with a credential-free diagnostic. Verify registry access and available disk space before retrying. A hard kill may leave an unused staging directory under `.rdsh/releases`; subsequent launches use a fresh directory and never treat it as complete. Incomplete and old version directories continue consuming space until the owner removes the unwanted installations while DSH is stopped.

## Startup, readiness and logs

The runtime selects the sole non-loopback IPv4 address. If a custom deployment attaches multiple networks, explicitly choose `DSH_BIND_INTERFACE`; never replace the bind address with `0.0.0.0`. PUBLIC_URL must be the public HTTPS origin with root path, including when TLS is handled by an external proxy.

The supervisor consumes DSH's startup URL privately. It validates the expected origin and token format, confirms HTTP readiness, then atomically publishes the handoff. Healthchecks confirm both the current generation and a reachable protected HTTP endpoint. They never exchange a token or print one. The handoff is cleared before startup and on graceful shutdown; after an abrupt container termination it can remain until the next launch, so the gateway must treat failed upstream exchange as unavailable.

DSH stdout/stderr and npm diagnostics are not relayed into Docker logs. Only wrapper lifecycle messages and allowlisted failure categories appear. DSH itself may save private startup diagnostics under `.dsh/logs`; early native failures may not produce such a file. These upstream diagnostics can contain configuration values and should be inspected locally by the owner, not pasted wholesale into bug reports.

SIGTERM/SIGINT cancel installation or stop the DSH process group. The supervisor escalates a DSH process that has not stopped after eight seconds; the container's init reaps orphaned processes. Configure a Compose stop grace period longer than this bound.

## Container restrictions

The image contains Node/npm, the pinned pnpm managed by Corepack, and the wrapper, but no preinstalled DSH, user account credentials or certificates. Runtime UID/GID must be positive non-root IDs. Use a read-only root filesystem, `cap_drop: [ALL]`, `no-new-privileges:true`, the default Docker seccomp profile and `/tmp` as `rw,nosuid,nodev,noexec` tmpfs. Only HOME and the dedicated handoff directory need persistent write access. The native addon cache remains under HOME because shared libraries require executable mappings.

The tested starting limits are 2 CPUs, 3 GiB RAM, 512 PIDs and a 512 MiB `/tmp`. Tune resources for actual workloads; these limits do not constrain the size of the host HOME directory. Allow at least 15 minutes of healthcheck startup grace for the first installation and at least 12 seconds for shutdown. Never mount Docker's socket or host credential directories and never use host PID/network namespaces, privileged mode or seccomp=unconfined to work around an upstream error.

The packaged Linux Landlock runner is functionally probed on the host kernel. It ran successfully with full reported enforcement on the tested ARM64 OrbStack kernel. This does not certify other kernels or all plugins, and it does not replace the container boundary. Missing sandbox support must remain a visible fail-closed error.

No published DSH port prevents direct inbound access; it does **not** block outbound access to the host, LAN or cloud metadata. Registry downloads, model APIs and user tools require outbound networking. For a server requiring that boundary, the administrator must enforce destination restrictions on this Docker network using its host's actual Docker firewall backend: block host/LAN/link-local/metadata destinations in IPv4 and IPv6, while allowing the intended DNS and public API/registry traffic. Account for any explicitly required private model endpoint. Verify from this container, not just from the host shell. The project does not modify host firewall rules or claim that a bridge network enforces those restrictions by itself.

## Runtime verification

Run `node --test tests/runtime/*.test.mjs` from the repository root for filesystem, installer and handoff checks. Installer failure tests inject offline and ENOSPC failures rather than filling the host disk; a test manifest exercises explicit version changes while preserving a history fixture. This validates wrapper behavior, not upstream data-migration compatibility between arbitrary DSH releases.

Build `rdsh-runtime:dev` with `docker build -t rdsh-runtime:dev dsh`, then run `python3 tests/runtime/lifecycle.py`. It installs the real locked DSH in an empty temporary volume, checks authenticated access, rejects a competing runtime, recreates the container, kills/restarts it, and verifies cookie persistence, token rotation, workspace retention and credential-free Docker logs. Only resources with that test's random prefix are removed. No model API key or paid request is required. Native sandbox and PTY checks were also executed against the published ARM64 package; real model calls and other Linux architectures require their own validation.
