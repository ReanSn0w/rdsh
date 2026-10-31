#!/usr/bin/env python3
"""Run a disposable real-DSH login lifecycle through a TLS reverse proxy.

Only artificial credentials and isolated Compose project volumes are used. No model
API is called. Requires Docker, Python 3 and openssl. Never prints cookies or tokens.
"""
import argparse
import http.client
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[2]


def command(*args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, **kwargs)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Browser:
    def __init__(self, port, certificate, authority=None):
        self.port = port
        self.authority = authority or f"dsh.example.test:{port}"
        self.cookies = {}
        self.context = ssl.create_default_context(cafile=str(certificate))

    def request(self, method, path, body=None, extra=None):
        raw = socket.create_connection(("127.0.0.1", self.port), timeout=20)
        conn = http.client.HTTPConnection(self.authority, timeout=20)
        conn.sock = self.context.wrap_socket(raw, server_hostname="dsh.example.test")
        headers = {"Host": self.authority, "Origin": f"https://{self.authority}", "Cookie": "; ".join(f"{key}={value}" for key, value in self.cookies.items())}
        if body is not None:
            body = urllib.parse.urlencode(body)
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        headers.update(extra or {})
        conn.request(method, path, body=body, headers=headers)
        response = conn.getresponse()
        status, result_headers = response.status, response.getheaders()
        data = response.read() if status != 101 else b""
        for key, value in result_headers:
            if key.lower() == "set-cookie":
                cookies = SimpleCookie()
                cookies.load(value)
                for name, morsel in cookies.items():
                    if morsel["max-age"] == "0" or not morsel.value:
                        self.cookies.pop(name, None)
                    else:
                        assert morsel["secure"] and morsel["httponly"], "insecure authentication cookie"
                        self.cookies[name] = morsel.value
        conn.close()
        return status, dict(result_headers), data

    def login(self):
        status, headers, page = self.request("GET", "/_rdsh/login")
        assert status == 200, f"login form status {status}"
        assert headers.get("Referrer-Policy") == "same-origin", "form policy would prevent browser Origin validation"
        csrf = re.search(rb'name="csrf" value="([^"]+)"', page)
        assert csrf, "missing login csrf"
        status, headers, _ = self.request("POST", "/_rdsh/login", {"username": "token", "password": "integration-test-password", "csrf": csrf[1].decode()})
        assert status == 303 and headers.get("Location") == "/", f"login exchange status {status}"
        status, _, _ = self.request("GET", "/")
        assert status == 200, f"authenticated root status {status}"


def host_proxy(port, upstream_port, certificate, key):
    class Proxy(BaseHTTPRequestHandler):
        def handle_request(self):
            upstream = http.client.HTTPConnection("127.0.0.1", upstream_port, timeout=30)
            headers = {key: value for key, value in self.headers.items() if key.lower() not in {"connection", "transfer-encoding"}}
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            upstream.request(self.command, self.path, body=body, headers=headers)
            response = upstream.getresponse()
            data = response.read()
            self.send_response(response.status)
            for name, value in response.getheaders():
                if name.lower() not in {"connection", "transfer-encoding", "content-length", "server", "date"}:
                    self.send_header(name, value)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            upstream.close()
        do_GET = do_POST = handle_request
        def log_message(self, *_):
            pass
    server = ThreadingHTTPServer(("127.0.0.1", port), Proxy)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certificate, key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--proxy-mode", choices=["host", "network", "auto"], default="host")
    parser.add_argument("--standard-public-ports", action="store_true", help="network mode only: existing test proxy occupies loopback 80/443 before Compose starts")
    parser.add_argument("--runtime-context", type=Path, default=ROOT / "dsh")
    parser.add_argument("--gateway-context", type=Path, default=ROOT / "gateway")
    args = parser.parse_args()
    if args.standard_public_ports and args.proxy_mode != "network":
        parser.error("--standard-public-ports requires --proxy-mode network")
    project = "rdsh-smoke-" + uuid.uuid4().hex[:10]
    uid, gid = (os.getuid() or 1000), (os.getgid() or 1000)
    http_port, tls_port, management_port = free_port(), free_port(), free_port()
    environment = {**os.environ, "PUBLIC_URL": f"https://dsh.example.test:{tls_port}/", "DSH_VERSION": "0.2.1-rc.5", "RDSH_UID": str(uid), "RDSH_GID": str(gid), "GATEWAY_HTTP_PORT": str(http_port), "PROXY_NETWORK": project + "-proxy", "TRUSTED_PROXIES": "", "ACME_EMAIL": "integration@example.test"}
    if args.standard_public_ports:
        tls_port = 443
    if args.proxy_mode == "auto" or args.standard_public_ports:
        environment["PUBLIC_URL"] = "https://dsh.example.test/"
    authority = urllib.parse.urlsplit(environment["PUBLIC_URL"]).netloc
    with tempfile.TemporaryDirectory(prefix="rdsh-smoke-") as directory:
        temporary = Path(directory).resolve()
        home = temporary / "home"
        home.mkdir(mode=0o700)
        if os.getuid() == 0:
            os.chown(home, uid, gid)
        certificate, key = temporary / "cert.pem", temporary / "key.pem"
        command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(certificate), "-days", "1", "-subj", "/CN=dsh.example.test", "-addext", "subjectAltName=DNS:dsh.example.test,DNS:pebble,DNS:localhost")
        # These credentials exist only for this isolated test deployment.
        password_file, session_file = temporary / "password_hash", temporary / "session_secret"
        for path in (password_file, session_file):
            path.touch(mode=0o600)
            if os.getuid() == 0:
                os.chown(path, uid, gid)
        override = temporary / "test.json"
        gateway_image, runtime_image = project + "-gateway", project + "-dsh"
        override.write_text(json.dumps({"services": {
            "gateway": {"image": gateway_image, "build": {"context": str(args.gateway_context.resolve())}},
            "dsh": {"image": runtime_image, "build": {"context": str(args.runtime_context.resolve())}, "volumes": [{"type": "bind", "source": str(home), "target": "/home/dsh"}, {"type": "volume", "source": "handoff", "target": "/run/rdsh"}]}},
            "secrets": {"password_hash": {"file": str(password_file)}, "session_secret": {"file": str(session_file)}}}))
        variant = ROOT / ("compose.proxy-host.yml" if args.proxy_mode == "host" else "compose.proxy-network.yml")
        if args.proxy_mode == "auto":
            # Same auto-mode override, only host ports differ to avoid other apps.
            variant = temporary / "auto.yml"
            variant.write_text((ROOT / "compose.https.yml").read_text().replace('"80:8080"', f'"127.0.0.1:{http_port}:8080"').replace('"443:8443"', f'"127.0.0.1:{tls_port}:8443"'))
            config = json.loads(override.read_text())
            config["services"]["gateway"].update({
                "environment": {"ACME_DIRECTORY_URL": "https://pebble:14000/dir", "SSL_CERT_FILE": "/run/test-ca.pem"},
                "networks": {"runtime": {"aliases": ["dsh.example.test"]}},
                "volumes": [{"type": "bind", "source": str(certificate), "target": "/run/test-ca.pem", "read_only": True}]})
            override.write_text(json.dumps(config))
        compose = ["docker", "compose", "--env-file", "/dev/null", "-p", project, "-f", str(ROOT / "docker-compose.yml"), "-f", str(variant), "-f", str(override)]
        server = None
        proxy_name = project + "-nginx"
        network_created = False
        started = False
        try:
            print(f"Building isolated images ({args.proxy_mode} proxy)", flush=True)
            command(*compose, "build", env=environment)
            if args.proxy_mode == "host":
                print(command(sys.executable, str(ROOT / "tests/integration/image-boundaries.py"), gateway_image, runtime_image).stdout.decode(), end="", flush=True)
            hashed = command("docker", "run", "--rm", "-i", "--network", "none", gateway_image, "hash-password", input=b"integration-test-password\n").stdout
            session = command("docker", "run", "--rm", "--network", "none", gateway_image, "generate-secret").stdout
            password_file.write_bytes(hashed)
            session_file.write_bytes(session)
            if args.proxy_mode == "network":
                command("docker", "network", "create", environment["PROXY_NETWORK"])
                network_created = True
            if args.proxy_mode == "network":
                config = (ROOT / "examples/nginx-docker.conf").read_text().replace("dsh.example.com", "dsh.example.test")
                if args.standard_public_ports:
                    config += "\nserver { listen 80; server_name dsh.example.test; return 308 https://dsh.example.test$request_uri; }\n"
                extra_ports = ["-p", "127.0.0.1:80:80"] if args.standard_public_ports else []
                config_file = temporary / "nginx.conf"
                config_file.write_text(config)
                command("docker", "run", "-d", "--name", proxy_name, "--network", environment["PROXY_NETWORK"], "-p", f"127.0.0.1:{tls_port}:443", *extra_ports, "--mount", f"type=bind,src={config_file},dst=/etc/nginx/conf.d/default.conf,readonly", "--mount", f"type=bind,src={certificate},dst=/etc/nginx/tls/fullchain.pem,readonly", "--mount", f"type=bind,src={key},dst=/etc/nginx/tls/privkey.pem,readonly", "nginx:1.28-alpine@sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236")
            started = True
            print("Starting the two services and installing real DSH", flush=True)
            command(*compose, "up", "-d", "--wait", "--wait-timeout", "900", env=environment)
            ids = command(*compose, "ps", "-q", env=environment).stdout.splitlines()
            assert len(ids) == 2, "expected exactly two production services"
            if args.proxy_mode == "host":
                server = host_proxy(tls_port, http_port, certificate, key)
            elif args.proxy_mode == "network":
                pass  # Proxy was started before Compose to model an existing deployment.
            else:
                config_file = temporary / "pebble.json"
                config_file.write_text(json.dumps({"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000", "certificate": "/test/cert.pem", "privateKey": "/test/key.pem", "httpPort": 8080, "tlsPort": 8443, "keyAlgorithm": "ecdsa", "profiles": {"default": {"description": "rdsh compose test", "validityPeriod": 3600}}, "retryAfter": {"authz": 0, "order": 0}}}))
                command("docker", "run", "-d", "--name", proxy_name, "--user", "0:0", "--network", project + "_runtime", "--network-alias", "pebble", "-p", f"127.0.0.1:{management_port}:15000", "--mount", f"type=bind,src={config_file},dst=/test/pebble.json,readonly", "--mount", f"type=bind,src={certificate},dst=/test/cert.pem,readonly", "--mount", f"type=bind,src={key},dst=/test/key.pem,readonly", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "-e", "PEBBLE_AUTHZREUSE=0", "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199", "-config", "/test/pebble.json", "-strict=false")
                for attempt in range(30):
                    try:
                        client = http.client.HTTPSConnection("localhost", management_port, timeout=5, context=ssl.create_default_context(cafile=str(certificate)))
                        client.request("GET", "/roots/0")
                        response = client.getresponse()
                        root_pem = response.read()
                        client.close()
                        if response.status == 200 and root_pem.startswith(b"-----BEGIN CERTIFICATE-----"):
                            break
                    except (OSError, http.client.HTTPException):
                        pass
                    time.sleep(1)
                else:
                    raise AssertionError("Pebble management root CA unavailable")
                certificate = temporary / "issued-root.pem"
                certificate.write_bytes(root_pem)
                redirect = http.client.HTTPConnection("127.0.0.1", http_port, timeout=5)
                redirect.request("GET", "/", headers={"Host": authority})
                response = redirect.getresponse()
                assert response.status in (301, 302, 307, 308) and response.getheader("Location").startswith(environment["PUBLIC_URL"])
                response.read()
                redirect.close()
            browser = Browser(tls_port, certificate, authority)
            for attempt in range(30):
                try:
                    status, _, _ = browser.request("GET", "/_rdsh/login")
                    if status == 200:
                        break
                except (OSError, http.client.HTTPException):
                    pass
                time.sleep(1)
            else:
                raise AssertionError("TLS proxy did not become ready")
            status, _, _ = browser.request("GET", "/api/nonexistent")
            assert status == 401, f"unauthenticated API status {status}"
            browser.login()
            status, _, _ = browser.request("GET", "/api/nonexistent")
            assert status == 404, f"authenticated API status {status}"
            if args.proxy_mode in ("network", "auto"):
                status, _, _ = browser.request("GET", "/api/remote.mux", extra={"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="})
                assert status == 101, f"authenticated WebSocket status {status}"
            marker = home / "persistence-check.txt"
            marker.write_text("synthetic persistence marker\n")
            print("Checking DSH recreation, preserved home and fresh-browser login", flush=True)
            command(*compose, "up", "-d", "--force-recreate", "--wait", "--wait-timeout", "180", "dsh", env=environment)
            assert marker.read_text() == "synthetic persistence marker\n"
            assert browser.request("GET", "/")[0] == 200, "existing browser session lost after DSH recreation"
            Browser(tls_port, certificate, authority).login()
            print("Checking logout and gateway session revocation", flush=True)
            status, _, page = browser.request("GET", "/_rdsh/logout")
            csrf = re.search(rb'name="csrf" value="([^"]+)"', page)
            assert status == 200 and csrf
            assert browser.request("POST", "/_rdsh/logout", {"csrf": csrf[1].decode()})[0] == 303
            assert browser.request("GET", "/api/nonexistent")[0] == 401
            browser.login()
            command(*compose, "up", "-d", "--force-recreate", "--wait", "--wait-timeout", "60", "gateway", env=environment)
            # Docker proxy refreshes its DNS cache after gateway recreation.
            for attempt in range(20):
                status = browser.request("GET", "/api/nonexistent")[0]
                if status == 401:
                    break
                time.sleep(1)
            assert status == 401, "gateway recreation did not revoke browser session"
            browser.login()
            logs = command(*compose, "logs", "--no-color", env=environment).stdout
            assert b"integration-test-password" not in logs and b"?token=" not in logs, "credentials found in service logs"
            print(f"PASS: real DSH login/lifecycle via {args.proxy_mode} TLS proxy; no model calls", flush=True)
        finally:
            if server:
                server.shutdown()
                server.server_close()
            subprocess.run(["docker", "rm", "-f", proxy_name], capture_output=True)
            if started:
                subprocess.run(compose + ["down", "-v", "--remove-orphans"], env=environment, capture_output=True)
            if network_created:
                subprocess.run(["docker", "network", "rm", environment["PROXY_NETWORK"]], capture_output=True)
            subprocess.run(["docker", "image", "rm", gateway_image, runtime_image], capture_output=True)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        # Do not dump command output; tests may have generated auth material.
        raise SystemExit(f"Integration command failed (exit {error.returncode}): {error.cmd}; output withheld to protect generated credentials") from None
