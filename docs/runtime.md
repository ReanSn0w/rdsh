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
