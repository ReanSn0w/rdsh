#!/usr/bin/env python3
"""Verify install -> hash command -> literal .env interpolation with real Docker."""
import json
from pathlib import Path
import subprocess
import tempfile
import uuid

from bootstrap_fixture import ROOT, fixture


def run(*args, **kwargs):
    return subprocess.run(args, capture_output=True, check=True, **kwargs)


def main():
    with tempfile.TemporaryDirectory(prefix="rdsh-bootstrap-smoke-") as directory:
        temporary = Path(directory).resolve()
        environment = fixture(temporary)
        destination = temporary / "deployment"
        project = "rdsh-bootstrap-" + uuid.uuid4().hex[:10]
        gateway_image = project + "-gateway"
        dsh_image = project + "-dsh"
        run("docker", "build", "-t", gateway_image, str(ROOT / "gateway"))
        run("docker", "build", "-t", dsh_image, str(ROOT / "dsh"))
        environment.update({"RDSH_GATEWAY_IMAGE": gateway_image, "RDSH_DSH_IMAGE": dsh_image})
        run("bash", str(ROOT / "scripts/install.sh"), str(destination), env=environment)
        compose = ["docker", "compose", "--env-file", str(destination / ".env"), "-p", project, "-f", str(destination / "docker-compose.yml")]
        probe = None
        try:
            config_file = destination / ".env"  # Our own disposable generated template only.
            config_file.write_text(config_file.read_text()
                .replace("PUBLIC_URL=https://dsh.example.com/", "PUBLIC_URL=")
                .replace("ghcr.io/reansn0w/rdsh-gateway:latest", gateway_image)
                .replace("ghcr.io/reansn0w/rdsh-dsh:latest", dsh_image))
            # Empty credentials allow a one-shot command, without starting DSH or ports.
            output = run(*compose, "run", "--rm", "--no-deps", "-T", "gateway", "hash-password", input=b"bootstrap-test-password\n").stdout
            # BuildKit progress may share stdout with the final CLI result.
            hashed = output.strip().splitlines()[-1].decode()
            assert hashed.startswith("$2") and len(hashed) == 60
            text = config_file.read_text().replace("AUTH_PASSWORD_HASH=''", "AUTH_PASSWORD_HASH='" + hashed + "'")
            config_file.write_text(text)
            config = json.loads(run(*compose, "config", "--format", "json").stdout)
            gateway = config["services"]["gateway"]
            # `config` serializes dollars as $$; inspect the actual created container too.
            assert gateway["environment"]["AUTH_PASSWORD_HASH"].replace("$$", "$") == hashed
            assert gateway["environment"]["TLS_MODE"] == "auto"
            assert {p["published"] for p in gateway["ports"]} == {"80", "443"}
            assert not gateway.get("secrets")
            assert not run(*compose, "ps", "-q").stdout.strip(), "hash command started persistent services"
            # A short-lived healthcheck command starts no server or dependencies.
            probe = run(*compose, "run", "--no-deps", "--detach", "gateway", "healthcheck").stdout.strip().decode()
            values = json.loads(run("docker", "inspect", "--format", "{{json .Config.Env}}", probe).stdout)
            actual = dict(value.split("=", 1) for value in values)
            assert actual["AUTH_PASSWORD_HASH"] == hashed, "Compose changed bcrypt in the container environment"
            print("PASS: Bash bootstrap, isolated hash command before first start, literal bcrypt .env, default HTTPS")
        finally:
            if probe:
                subprocess.run(["docker", "rm", "-f", probe], capture_output=True)
            subprocess.run(compose + ["down", "-v"], capture_output=True)
            subprocess.run(["docker", "image", "rm", gateway_image, dsh_image], capture_output=True)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"Bootstrap smoke failed (exit {error.returncode}); generated credentials withheld") from None
