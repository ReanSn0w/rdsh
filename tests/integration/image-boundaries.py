#!/usr/bin/env python3
"""Inspect fresh image metadata/layer filenames without reading credential files."""
import argparse
import json
from pathlib import Path, PurePosixPath
import subprocess
import tarfile
import tempfile


def check(image):
    metadata = json.loads(subprocess.check_output(["docker", "image", "inspect", image]))[0]
    config = metadata["Config"]
    assert config["User"] and config["User"].split(":")[0] not in ("0", "root"), "root image user"
    forbidden_env = {"AUTH_PASSWORD", "PASSWORD", "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "SESSION_SECRET", "DSH_TOKEN"}
    for item in config.get("Env", []):
        name = item.partition("=")[0]
        assert name not in forbidden_env, "credential value configured in image environment"
    forbidden_files = {".env", "password_hash", "session_secret", "connection.json", ".credentials.yaml", "privkey.pem", "id_rsa", "id_ed25519"}
    with tempfile.TemporaryDirectory(prefix="rdsh-image-check-") as directory:
        archive_path = Path(directory) / "image.tar"
        subprocess.run(["docker", "image", "save", "-o", str(archive_path), image], check=True, capture_output=True)
        count = 0
        with tarfile.open(archive_path) as archive:
            manifest = json.load(archive.extractfile("manifest.json"))
            for entry in manifest:
                for layer_name in entry["Layers"]:
                    # Each layer is checked, including files removed by later layers.
                    # Only metadata is inspected; possible credential contents are not opened.
                    with tarfile.open(fileobj=archive.extractfile(layer_name), mode="r|*") as layer:
                        for member in layer:
                            if member.isfile():
                                count += 1
                                assert PurePosixPath(member.name).name not in forbidden_files, "credential file found in image layer"
        print(f"PASS: {image}: non-root image, {count} layer file entries, no deployment credential files or credential-value env settings")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("images", nargs="+")
    for image in parser.parse_args().images:
        check(image)
