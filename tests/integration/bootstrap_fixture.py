"""Serve the current source archive through a fake curl, without a remote push."""
import os
from pathlib import Path
import tarfile

ROOT = Path(__file__).resolve().parents[2]


def fixture(directory):
    archive = directory / "source.tar.gz"
    with tarfile.open(archive, "w:gz") as output:
        for name in ["docker-compose.yml", ".env.example"]:
            output.add(ROOT / name, arcname="rdsh-master/" + name)
    binaries = directory / "bin"
    binaries.mkdir()
    curl = binaries / "curl"
    curl.write_text('''#!/bin/bash
set -eu
destination=''
while [[ $# -gt 0 ]]; do
  if [[ "$1" == --output ]]; then destination=$2; shift; fi
  shift
done
cp "$RDSH_TEST_ARCHIVE" "$destination"
''')
    curl.chmod(0o700)
    return {**os.environ, "PATH": str(binaries) + os.pathsep + os.environ["PATH"], "RDSH_TEST_ARCHIVE": str(archive)}
