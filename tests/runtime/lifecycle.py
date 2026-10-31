#!/usr/bin/env python3
"""Docker lifecycle acceptance test; creates and removes only its own resources.

Build rdsh-runtime:dev first. No model key, API call or host port is needed.
The first run downloads the locked npm release. Requires Docker and Python 3.
"""
import argparse
import json
from pathlib import Path
import subprocess
import time
import uuid


def docker(*args, input=None, check=True):
    result = subprocess.run(["docker", *args], input=input, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True)
    if check and result.returncode:
        raise RuntimeError(f"docker {args[0]} failed (exit {result.returncode}); test output withheld")
    return result


def wait_ready(name):
    deadline = time.monotonic() + 900
    while time.monotonic() < deadline:
        state = json.loads(docker("inspect", name).stdout)[0]["State"]
        if not state["Running"]:
            raise RuntimeError(f"runtime stopped before ready (exit {state['ExitCode']})")
        if state.get("Health", {}).get("Status") == "healthy":
            return
        time.sleep(2)
    raise RuntimeError("runtime readiness timeout")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="rdsh-runtime:dev")
    args = parser.parse_args()
    prefix = "rdsh-lifecycle-" + uuid.uuid4().hex[:10]
    name, network, home, handoff = prefix, prefix+"-net", prefix+"-home", prefix+"-handoff"
    client = Path(__file__).with_name("lifecycle-client.mjs")
    restrictions = ["--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                    "--pids-limit", "512", "--memory", "3g", "--cpus", "2",
                    "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=512m"]
    mounts = ["--mount", f"type=volume,source={home},target=/home/dsh",
              "--mount", f"type=volume,source={handoff},target=/run/rdsh"]

    def copy_client():
        # docker cp rejects read-only root filesystems even for a writable tmpfs.
        docker("exec", "-i", name, "node", "--input-type=module", "-e",
               "import{writeFile}from'node:fs/promises';let data='';for await(const c of process.stdin)data+=c;await writeFile('/tmp/lifecycle-client.mjs',data,{mode:0o600});",
               input=client.read_text())

    def create():
        docker("run", "-d", "--name", name, "--network", network, *restrictions, *mounts,
               "--env", "PUBLIC_URL=https://rdsh-lifecycle.example", args.image)
        wait_ready(name)
        copy_client()

    def verify(mode):
        result = docker("exec", name, "node", "/tmp/lifecycle-client.mjs", mode)
        print(result.stdout.strip(), flush=True)

    try:
        docker("network", "create", network)
        create()
        version = docker("exec", name, "dsh", "--version").stdout.strip()
        assert version == "0.2.1-rc.5", f"dsh is not available on PATH (reported {version!r})"
        pnpm_version = docker("exec", name, "pnpm", "--version").stdout.strip()
        assert pnpm_version == "11.28.5", f"pinned pnpm is unavailable (reported {pnpm_version!r})"
        print("PASS: dsh and pinned pnpm commands are available on PATH", flush=True)
        verify("record")
        config = json.loads(docker("inspect", name).stdout)[0]
        assert config["Config"]["User"] != "0:0"
        assert config["HostConfig"]["ReadonlyRootfs"]
        assert config["HostConfig"]["CapDrop"] == ["ALL"]
        assert not config["HostConfig"]["PortBindings"]
        second = docker("run", "--rm", "--network", network, *restrictions, *mounts,
                        "--env", "PUBLIC_URL=https://rdsh-lifecycle.example", args.image, check=False)
        assert second.returncode == 75, "concurrent HOME use must fail with lock contention"
        print("PASS: concurrent runtime rejected without disturbing active home", flush=True)
        docker("stop", "--timeout", "12", name)
        absent = docker("run", "--rm", "--network", "none", "--entrypoint", "node", "--mount",
                        f"type=volume,source={handoff},target=/run/rdsh,readonly", args.image, "-e",
                        "if(require('fs').existsSync('/run/rdsh/connection.json'))process.exit(1)")
        assert absent.returncode == 0
        docker("rm", name)
        create()
        verify("compare")
        logs = docker("logs", name).stdout
        assert "using installed DSH version" in logs
        assert "installing pinned DSH dependencies" not in logs
        print("PASS: recreation reuses installation without npm reinstall", flush=True)
        verify("record")
        docker("kill", "--signal", "KILL", name)
        docker("start", name)
        wait_ready(name)
        copy_client()
        verify("compare")
        all_logs = docker("logs", name)
        checked = docker("exec", "-i", name, "node", "/tmp/lifecycle-client.mjs", "logs",
                         input=all_logs.stdout+all_logs.stderr)
        print(checked.stdout.strip(), flush=True)
        verify("clean")
        print("PASS: runtime lifecycle acceptance complete", flush=True)
    finally:
        docker("rm", "-f", name, check=False)
        for volume in [home, handoff]:
            docker("volume", "rm", volume, check=False)
        docker("network", "rm", network, check=False)


if __name__ == "__main__":
    main()
