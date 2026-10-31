#!/usr/bin/env bash
# Bootstrap a fresh standalone deployment. Compatible with Bash 3.2+.
set -euo pipefail
umask 077

die() { printf 'rdsh: %s\n' "$*" >&2; exit 1; }
[[ $# -le 1 ]] || die 'Usage: bash install.sh [new-directory]'
target=${1:-./rdsh}
[[ -n "$target" && "$target" != */ ]] || die 'Choose a new directory without a trailing slash'
case "/$target/" in
  */.ssh/*|*/.aws/*|*/.gnupg/*|*/.kube/*|*/.codex/*|*/.git/*)
    die 'Do not install into a credential or application configuration directory' ;;
esac
[[ ! -e "$target" && ! -L "$target" ]] || die 'Destination already exists; nothing was changed'
for utility in curl tar docker mktemp cp chmod; do
  command -v "$utility" >/dev/null 2>&1 || die "Required command not found: $utility"
done
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required'

# Resolve only the parent; never follow a destination symlink or inspect credentials.
parent=$(cd -- "$(dirname -- "$target")" && pwd -P) || die 'Destination parent must exist'
target="$parent/$(basename -- "$target")"
case "$target/" in
  */.ssh/*|*/.aws/*|*/.gnupg/*|*/.kube/*|*/.codex/*|*/.git/*)
    die 'Do not install into a credential or application configuration directory' ;;
esac
[[ ! -e "$target" && ! -L "$target" ]] || die 'Destination already exists; nothing was changed'
temporary=$(mktemp -d "$parent/.rdsh-install.XXXXXX")
cleanup() { rm -rf -- "$temporary"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Downloading rdsh from master...\n'
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --connect-timeout 15 --max-time 180 \
  https://codeload.github.com/ReanSn0w/rdsh/tar.gz/refs/heads/master \
  --output "$temporary/source.tar.gz"
# One archive keeps the Compose file and its configuration template at one revision.
tar -xzf "$temporary/source.tar.gz" -C "$temporary" \
  rdsh-master/docker-compose.yml rdsh-master/.env.example
payload="$temporary/rdsh-master"
cp "$payload/.env.example" "$payload/.env"
rm -- "$payload/.env.example"
chmod 600 "$payload/.env"
mkdir -p "$payload/data/home"
chmod 700 "$payload/data" "$payload/data/home"
# mkdir reserves a previously absent destination; cp cannot overwrite an old install.
mkdir -- "$target" || die 'Destination appeared during installation; nothing was overwritten'
cp -pR "$payload/." "$target/"
(
  cd -- "$target"
  # Only the new dedicated HOME is mounted. The published image supplies chown.
  docker compose run --rm --no-deps --user 0:0 --cap-add CHOWN --entrypoint chown dsh 1000:1000 /home/dsh
)
printf '\nCreated %s\n' "$target"
printf '%s\n' \
  '1. Open .env and set PUBLIC_URL and ACME_EMAIL.' \
  '2. Generate a hash: docker compose run --rm --no-deps gateway hash-password' \
  "3. Set AUTH_PASSWORD_HASH in .env, enclosed in single quotes to preserve dollar signs." \
  '4. Point DNS to this server, allow ports 80/443, then run: docker compose up -d' \
  'Run Docker commands from the created directory. No service has been started.'
