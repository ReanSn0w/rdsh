# Validation record

## Simplified Bash bootstrap (2026-10-31)

The current deployment uses standalone HTTPS by default, an environment bcrypt
hash and a per-process signing key. Python is only a development/test dependency.

- Nine Compose/bootstrap checks passed, including existing-destination refusal,
  symlink/credential-path rejection and cleanup on failed download.
- The real Bash 3.2 installer was exercised with the deployment files from the
  current source archive. `docker compose run --rm --no-deps gateway hash-password` works before
  PUBLIC_URL and credentials are filled in, without starting DSH or public ports.
- A synthetic single-quoted bcrypt hash survived `.env` interpolation unchanged
  in the actual container environment. `compose config` escapes dollars as `$$`
  in its serialized output; the test also inspects its own disposable container.
- An isolated setup run verified HOME ownership 1000:1000 and modes 0700 for HOME
  / 0600 for the generated `.env`.
- Gateway race tests and `go vet` passed, including environment/file conflicts,
  missing/malformed hashes and independent random signing keys on each start.
- All three real-DSH lifecycle smoke tests passed again on Linux ARM64: default
  ACME HTTPS, host proxy and Docker-network proxy. They now use the environment
  hash and automatic signing key; no model calls or production ACME are used.

CI runs the updated bootstrap and deployment tests on Linux AMD64 for every push;
consult the [current workflow runs](https://github.com/ReanSn0w/rdsh/actions).

## Initial implementation baseline

Local validation on 2026-10-31 used Docker Engine 29.8.2 / Compose 5.5.0 on
OrbStack, with Linux ARM64 container images. The real upstream runtime was
`@deepseek-ai/dsh@0.2.1-rc.5` on Node.js 24.22.1. No model API calls were made.

| Check | Result |
| --- | --- |
| Four rendered Compose variants and eight configuration/setup boundary tests | Passed |
| Real setup helper: create private files, rotate password, preserve home/session secret | Passed |
| Custom UID 501 and existing base-image GID 20 | Passed |
| nginx host/network examples: `nginx -t` | Passed |
| Host reverse proxy with trusted TLS and real DSH | Passed |
| Docker nginx reverse proxy with trusted TLS and WebSocket upgrade | Passed |
| Existing Docker proxy bound to loopback 80/443 before Compose startup | Passed |
| Automatic HTTPS with isolated Pebble ACME, real TLS-ALPN domain validation and trusted issued chain | Passed |
| Non-root image metadata and every layer's file list: no deployment credential files or credential-value environment variables | Passed |

Each full `smoke.py` run verifies unauthenticated denial, login, native DSH cookie
exchange, API access, DSH recreation with the same home, existing browser cookie
persistence, a fresh browser login after DSH recreation, logout and gateway-restart
session revocation. Network and automatic TLS modes also verify authenticated
WebSocket upgrade. The host run was repeated after the browser form Referrer-Policy
fix, and the login helper now asserts that regression directly.

`setup-smoke.py` supplies synthetic interactive input to the real setup helper and
uses the real gateway Docker CLI. No existing user credential files are opened.
All deployments use unique test project names and temporary homes. Cleanup removes
only those test containers, named volumes, images and temporary files.

The automatic HTTPS fixture uses the production Compose override with only its
host port mappings changed for isolation. A temporary override adds a test ACME
directory and test trust root. The fixture does not skip domain validation or TLS
certificate verification. Public DNS and Let's Encrypt production issuance for an
owner's real domain are environment-dependent and were not tested here.

The additional test with an already-running external proxy is available explicitly:

```sh
python3 tests/integration/smoke.py --proxy-mode network --standard-public-ports
```

It requires loopback 80/443 to be available to Docker. It never stops an existing
service to acquire them. Ordinary CI smoke runs use high ephemeral host ports.

GitHub Actions passed unit/configuration checks and all three real-DSH proxy
modes on Linux AMD64 on 2026-10-31:
[run 41201184470](https://github.com/ReanSn0w/rdsh/actions/runs/41201184470),
commit `9ce08b682db8ae2c67d93f956580e20505e527b9`. This independently verifies
the host-proxy, Docker-network proxy and automatic HTTPS deployments on AMD64.
Other architectures are not implied by these ARM64 and AMD64 results.

Action references were resolved against their official repositories with
`git ls-remote` before being pinned to commit IDs. Component-specific tests and
security decisions are documented in [runtime](runtime.md), [gateway](gateway.md)
and [DSH integration](dsh-integration.md).
