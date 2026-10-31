#!/usr/bin/env python3
"""Prepare rdsh directories and credentials without putting secrets in argv."""
from __future__ import annotations

import argparse
import getpass
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def run(*args: str, **kwargs: object) -> subprocess.CompletedProcess:
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def configuration() -> tuple[int, int, Path, str]:
    result = run("docker", "compose", "-f", "docker-compose.yml", "config", "--format", "json", capture_output=True, text=True)
    config = json.loads(result.stdout)
    services = config["services"]
    owner = services["gateway"]["user"]
    if not re.fullmatch(r"[1-9][0-9]*:[1-9][0-9]*", owner):
        raise ValueError("RDSH_UID and RDSH_GID must be positive numeric IDs; root is not supported")
    uid, gid = map(int, owner.split(":"))
    if services["dsh"]["user"] != owner:
        raise ValueError("gateway and dsh must have the same UID/GID")
    home = next(v["source"] for v in services["dsh"]["volumes"] if v["target"] == "/home/dsh")
    return uid, gid, Path(home), services["gateway"]["image"]


def reject_symlinks(path: Path) -> None:
    for part in (path, *path.parents):
        if part.is_symlink():
            raise ValueError(f"Refusing symlink path: {part}")


def validate_home(home: Path) -> None:
    if not home.is_absolute():
        raise ValueError("Compose must resolve DSH_HOME_PATH to an absolute path")
    reject_symlinks(home)
    if any(part in {".ssh", ".aws", ".gnupg", ".kube", ".codex"} for part in home.parts):
        raise ValueError("Credential and application configuration directories are not a DSH home")
    if home == ROOT or home in ROOT.parents or home == Path.home():
        raise ValueError("Use a dedicated DSH home, not the repository or your real home")
    if home.exists() and not home.is_dir():
        raise ValueError("DSH_HOME_PATH is not a directory")


def ensure_directory(path: Path, uid: int, gid: int) -> None:
    reject_symlinks(path)
    if path.exists():
        info = path.stat()
        if not stat.S_ISDIR(info.st_mode):
            raise ValueError(f"Not a directory: {path}")
        if (info.st_uid, info.st_gid) != (uid, gid):
            raise ValueError(f"Existing directory {path} must be owned by {uid}:{gid}; adjust its owner explicitly, without recursive chown")
        if stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError(f"Existing directory {path} must have mode 0700; run chmod 700 on that directory")
        return
    path.mkdir(mode=0o700, parents=True)
    os.chmod(path, 0o700)
    if (os.geteuid(), os.getegid()) != (uid, gid):
        os.chown(path, uid, gid)


def verify_secret(path: Path, uid: int, gid: int) -> None:
    reject_symlinks(path)
    info = path.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError(f"Refusing non-regular or hard-linked secret file: {path}")
    if (info.st_uid, info.st_gid) != (uid, gid) or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError(f"Secret {path.name} must have owner {uid}:{gid} and mode 0600")


def write_secret(path: Path, value: bytes, uid: int, gid: int) -> None:
    descriptor, temporary = tempfile.mkstemp(prefix=".rdsh-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as output:
            os.fchmod(output.fileno(), 0o600)
            if (os.geteuid(), os.getegid()) != (uid, gid):
                os.fchown(output.fileno(), uid, gid)
            output.write(value.rstrip(b"\r\n") + b"\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rotate-password", action="store_true", help="replace only the password hash; restart gateway afterward")
    args = parser.parse_args()
    os.umask(0o077)
    uid, gid, home, image = configuration()
    validate_home(home)
    secrets = ROOT / "secrets"
    reject_symlinks(secrets)
    if os.geteuid() != 0 and (uid != os.geteuid() or gid not in os.getgroups() + [os.getegid()]):
        raise ValueError("Configured UID/GID are not your user/group. Set RDSH_UID and RDSH_GID to id -u/id -g in .env, or run setup as root to prepare a dedicated non-root owner")
    # Validate all existing paths before prompting or changing credentials.
    for directory in (home, secrets):
        if directory.exists():
            ensure_directory(directory, uid, gid)
    password_file = secrets / "password_hash"
    session_file = secrets / "session_secret"
    for path in (password_file, session_file):
        if path.exists() or path.is_symlink():
            verify_secret(path, uid, gid)
    if args.rotate_password:
        if not password_file.exists() or not session_file.exists():
            raise ValueError("Initial setup is required before password rotation")
    elif password_file.exists() or session_file.exists():
        raise ValueError("Credentials already exist; nothing was overwritten. Use --rotate-password to replace the password")
    run("docker", "compose", "-f", "docker-compose.yml", "build", "gateway")
    if not sys.stdin.isatty():
        raise ValueError("Run setup in an interactive terminal; do not pass passwords as command arguments")
    password = getpass.getpass("Gateway password (at least 12 characters): ")
    if len(password) < 12 or len(password.encode()) > 72:
        raise ValueError("Password must contain at least 12 characters and at most 72 UTF-8 bytes")
    if password != getpass.getpass("Repeat password: "):
        raise ValueError("Passwords do not match")
    hashed = run("docker", "run", "--rm", "-i", "--network", "none", image, "hash-password", input=(password + "\n").encode(), capture_output=True).stdout.strip()
    password = ""
    if not re.fullmatch(rb"\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}", hashed):
        raise ValueError("Gateway returned an invalid password hash; credentials were not written")
    session = None
    if not args.rotate_password:
        session = run("docker", "run", "--rm", "--network", "none", image, "generate-secret", capture_output=True).stdout.strip()
        if not re.fullmatch(rb"[A-Za-z0-9_-]{43}", session):
            raise ValueError("Gateway returned an invalid session secret; credentials were not written")
    ensure_directory(home, uid, gid)
    ensure_directory(secrets, uid, gid)
    if session is not None:
        write_secret(session_file, session, uid, gid)
    write_secret(password_file, hashed, uid, gid)
    if args.rotate_password:
        print("Password changed. Recreate gateway with your selected Compose override to revoke all sessions and reload the bind-mounted hash.")
    else:
        print("DSH home and credentials prepared. Start Compose with one publication override.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, json.JSONDecodeError, KeyError, StopIteration) as error:
        # Child stdout/stderr can contain credential data; never echo either here.
        message = "Docker command failed; check Docker availability and your non-secret Compose settings" if isinstance(error, subprocess.CalledProcessError) else str(error)
        print(f"Setup failed: {message}", file=sys.stderr)
        sys.exit(1)
