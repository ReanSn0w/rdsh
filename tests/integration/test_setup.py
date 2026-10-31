"""Protect setup from replacing credentials or repurposing sensitive paths."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("setup", ROOT / "scripts" / "setup.py")
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)


class SetupSafety(unittest.TestCase):
    def test_rejects_home_and_credentials_directories(self):
        for path in [Path.home(), ROOT, Path("/"), Path.home() / ".ssh", Path.home() / ".aws" / "dsh"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                setup.validate_home(path)

    def test_symlink_and_hardlink_secret_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            target = root / "target"
            target.write_bytes(b"synthetic")
            target.chmod(0o600)
            link = root / "link"
            link.symlink_to(target)
            with self.assertRaises(ValueError):
                setup.verify_secret(link, os.getuid(), os.getgid())
            link.unlink()
            os.link(target, link)
            with self.assertRaises(ValueError):
                setup.verify_secret(target, os.getuid(), os.getgid())

    def test_write_is_private_and_replaces_atomically(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp).resolve() / "secret"
            setup.write_secret(target, b"synthetic-test-value", os.getuid(), os.getgid())
            setup.verify_secret(target, os.getuid(), os.getgid())
            inode = target.stat().st_ino
            setup.write_secret(target, b"replacement-test-value", os.getuid(), os.getgid())
            self.assertNotEqual(target.stat().st_ino, inode)
            self.assertEqual(target.read_bytes(), b"replacement-test-value\n")

    def test_existing_secrets_cannot_be_overwritten_without_rotation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            home = root / "home"
            secrets = root / "secrets"
            for directory in (home, secrets):
                directory.mkdir(mode=0o700)
            for name in ("password_hash", "session_secret"):
                setup.write_secret(secrets / name, b"synthetic", os.getuid(), os.getgid())
            with patch.object(setup, "ROOT", root), patch.object(setup, "configuration", return_value=(os.getuid(), os.getgid(), home, "test-image")), patch("sys.argv", ["setup.py"]), patch.object(setup, "run") as run:
                with self.assertRaisesRegex(ValueError, "already exist"):
                    setup.main()
                run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
