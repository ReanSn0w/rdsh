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

Installation has bounded npm network retries and a ten-minute process timeout. Failed first install exits nonzero with a credential-free diagnostic. Verify registry access and available disk space before retrying. A hard kill may leave an unused staging directory under `.rdsh/releases`; subsequent launches use a fresh directory and never treat it as complete. Incomplete and old version directories continue consuming space until the owner removes the unwanted installations while DSH is stopped.
