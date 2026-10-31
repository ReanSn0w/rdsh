"""Run the real Bash installer against a local archive of the current sources."""
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest

from bootstrap_fixture import ROOT, fixture


class BootstrapSafety(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="rdsh-bootstrap-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.env = fixture(self.root)
        # The installer must not start Docker workloads.
        docker = self.root / "bin/docker"
        docker.write_text('#!/bin/sh\n[ "$*" = "compose version" ]\n')
        docker.chmod(0o700)
        self.target = self.root / "new deployment"

    def install(self, target=None):
        return subprocess.run(["bash", str(ROOT / "scripts/install.sh"), str(target or self.target)], env=self.env, capture_output=True, text=True)

    def test_creates_private_configuration_and_owned_home(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        config = self.target / ".env"
        self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)
        text = config.read_text()  # Only this test's newly generated template.
        self.assertIn("AUTH_PASSWORD_HASH=''", text)
        self.assertIn(f"RDSH_UID={os.getuid() or 1000}\n", text)
        home = self.target / "data/home"
        self.assertEqual(stat.S_IMODE(home.stat().st_mode), 0o700)
        self.assertEqual(home.stat().st_uid, os.getuid() or 1000)
        self.assertTrue((self.target / "gateway/Dockerfile").exists())
        self.assertTrue((self.target / "dsh/releases/0.2.1-rc.5/package-lock.json").exists())
        self.assertFalse((self.target / "scripts").exists())
        self.assertFalse((self.target / ".git").exists())
        self.assertFalse(list(self.root.glob(".rdsh-install.*")))

    def test_existing_install_is_not_changed(self):
        self.target.mkdir()
        marker = self.target / "keep"
        marker.write_text("unchanged")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.target.iterdir()), [marker])
        self.assertEqual(marker.read_text(), "unchanged")

    def test_symlink_and_credential_destination_rejected(self):
        self.target.symlink_to(self.root / "missing")
        self.assertNotEqual(self.install().returncode, 0)
        private = self.root / ".ssh"
        private.mkdir()
        self.assertNotEqual(self.install(private / "rdsh").returncode, 0)
        alias = self.root / "alias"
        alias.symlink_to(private, target_is_directory=True)
        self.assertNotEqual(self.install(alias / "rdsh").returncode, 0)
        self.assertFalse(list(private.iterdir()))

    def test_download_failure_cleans_staging_without_creating_install(self):
        self.env["RDSH_TEST_ARCHIVE"] = str(self.root / "not-found")
        self.assertNotEqual(self.install().returncode, 0)
        self.assertFalse(self.target.exists())
        self.assertFalse(list(self.root.glob(".rdsh-install.*")))


if __name__ == "__main__":
    unittest.main()
