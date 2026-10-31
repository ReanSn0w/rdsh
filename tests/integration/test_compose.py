"""Validate rendered deployment boundaries without reading local secrets."""
import json
import os
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


def rendered(override=None, **settings):
    env = {**os.environ, "PUBLIC_URL": "https://dsh.example.test/", "DSH_VERSION": "0.2.1-rc.5", "ACME_EMAIL": "test@example.test", "PROXY_NETWORK": "test-proxy", "RDSH_UID": "1234", "RDSH_GID": "1234", **settings}
    command = ["docker", "compose", "--env-file", "/dev/null", "-f", str(ROOT / "docker-compose.yml")]
    if override:
        command += ["-f", str(ROOT / override)]
    result = subprocess.run(command + ["config", "--format", "json"], env=env, capture_output=True, text=True, check=True)
    return json.loads(result.stdout)


class ComposeBoundaries(unittest.TestCase):
    def test_base_publishes_nothing(self):
        config = rendered()
        self.assertEqual(set(config["services"]), {"gateway", "dsh"})
        for service in config["services"].values():
            self.assertFalse(service.get("ports"))
            self.assertTrue(service["read_only"])
            self.assertEqual(service["user"], "1234:1234")
            self.assertEqual(service["cap_drop"], ["ALL"])
            self.assertFalse(service.get("privileged", False))
            self.assertNotEqual(service.get("network_mode"), "host")
        dsh = config["services"]["dsh"]
        gateway = config["services"]["gateway"]
        self.assertFalse(dsh.get("secrets"))
        self.assertEqual([v["target"] for v in dsh["volumes"]], ["/home/dsh", "/run/rdsh"])
        self.assertFalse(any(v["target"] == "/home/dsh" for v in gateway["volumes"]))
        self.assertTrue(next(v for v in gateway["volumes"] if v["target"] == "/run/rdsh")["read_only"])
        self.assertEqual(gateway["environment"]["TLS_MODE"], "external")

    def test_https_only_gateway_has_public_ports(self):
        config = rendered("compose.https.yml")
        self.assertFalse(config["services"]["dsh"].get("ports"))
        gateway = config["services"]["gateway"]
        self.assertEqual(gateway["environment"]["TLS_MODE"], "auto")
        self.assertEqual({p["published"] for p in gateway["ports"]}, {"80", "443"})

    def test_host_proxy_publishes_only_loopback(self):
        config = rendered("compose.proxy-host.yml", GATEWAY_HTTP_PORT="18080")
        gateway = config["services"]["gateway"]
        self.assertEqual(gateway["environment"]["TLS_MODE"], "external")
        self.assertEqual(len(gateway["ports"]), 1)
        self.assertEqual(gateway["ports"][0]["host_ip"], "127.0.0.1")
        self.assertEqual(gateway["ports"][0]["published"], "18080")
        self.assertNotIn("ACME_EMAIL", gateway["environment"])

    def test_network_proxy_never_attaches_dsh(self):
        config = rendered("compose.proxy-network.yml")
        self.assertEqual(set(config["services"]["dsh"]["networks"]), {"runtime"})
        self.assertEqual(set(config["services"]["gateway"]["networks"]), {"runtime", "proxy"})
        self.assertTrue(config["networks"]["proxy"]["external"])
        for service in config["services"].values():
            self.assertFalse(service.get("ports"))


if __name__ == "__main__":
    unittest.main()
