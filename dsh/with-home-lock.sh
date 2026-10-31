#!/bin/sh
set -eu
umask 077
node "$(dirname "$0")/prepare-home.mjs"
# Kernel-owned lock survives normal startup races and releases on process exit.
# Exit 75 means another runtime is already using this same dedicated HOME.
exec flock --nonblock --conflict-exit-code 75 --no-fork "$HOME/.rdsh/runtime.lock" "$@"
