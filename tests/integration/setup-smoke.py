#!/usr/bin/env python3
"""Exercise real setup Docker CLI using synthetic interactive input in a temp root."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("setup", ROOT / "scripts/setup.py")
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway-context", type=Path, default=ROOT / "gateway")
    args = parser.parse_args()
    uid, gid = (os.getuid() or 1000), (os.getgid() or 1000)
    with tempfile.TemporaryDirectory(prefix="rdsh-setup-smoke-") as directory:
        temporary = Path(directory).resolve()
        image = "rdsh-setup-smoke:local"
        config = {"services": {
            "gateway": {"image": image, "build": {"context": str(args.gateway_context.resolve()), "args": {"RDSH_UID": str(uid), "RDSH_GID": str(gid)}}, "user": f"{uid}:{gid}"},
            "dsh": {"image": "unused-runtime", "user": f"{uid}:{gid}", "volumes": [{"type": "bind", "source": str(temporary / "home"), "target": "/home/dsh"}]}}}
        (temporary / "docker-compose.yml").write_text(json.dumps(config))
        try:
            with patch.object(setup, "ROOT", temporary), patch("sys.stdin.isatty", return_value=True), patch("getpass.getpass", return_value="synthetic-initial-password"), patch("sys.argv", ["setup.py"]):
                setup.main()
            for name in ("password_hash", "session_secret"):
                setup.verify_secret(temporary / "secrets" / name, uid, gid)
            old_hash = (temporary / "secrets/password_hash").read_bytes()
            old_session = (temporary / "secrets/session_secret").read_bytes()
            marker = temporary / "home/marker"
            marker.write_text("synthetic data")
            with patch.object(setup, "ROOT", temporary), patch("sys.stdin.isatty", return_value=True), patch("getpass.getpass", return_value="synthetic-updated-password"), patch("sys.argv", ["setup.py", "--rotate-password"]):
                setup.main()
            assert (temporary / "secrets/password_hash").read_bytes() != old_hash
            assert (temporary / "secrets/session_secret").read_bytes() == old_session
            assert marker.read_text() == "synthetic data"
            print("PASS: setup creates private secrets, password rotation preserves session secret and home")
        finally:
            subprocess.run(["docker", "image", "rm", image], capture_output=True)


if __name__ == "__main__":
    main()
