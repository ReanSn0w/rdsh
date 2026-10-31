# DSH integration contract

Verified on 2026-10-31 against the published `@deepseek-ai/dsh@0.2.1-rc.5`, not the moving upstream `master` branch. The [official repository](https://github.com/deepseek-ai/deepseek-harness) owns DSH. This project provides its deployment wrapper.

## Verified runtime

- Base: `node:24.22.1-bookworm-slim` (Debian glibc); published Node image digest observed: `sha256:be8204d4bd327363357f532187b752ff62759c31bedf98e9e18fc08c9cbd605c`.
- Tested platform: Linux ARM64, Docker 29.8.2 on OrbStack. AMD64 packages exist upstream but have not yet been exercised here.
- Installation: the published npm package and all resolved dependencies are pinned by the bundled package lock. DSH is installed in the mounted home at runtime, not baked into the image. A new supported DSH version requires a reviewed lock and image update.
- The base ships Bash. The deployment image adds CA certificates, Git, tini and util-linux for its runtime lock. It does not include a general compiler toolchain or Python environment.
- Native addons cannot load from a `noexec` `/tmp`: `node-addon-native-custom-loader` normally copies binaries there. Set its supported `NARB_NATIVE_CACHE_DIR` to `/home/dsh/.cache/native`, preserving `noexec` on `/tmp`.
- With capabilities dropped, no-new-privileges and Docker's default seccomp profile, the packaged Landlock launcher reported `full` enforcement on this kernel and successfully executed Bash. Other hosts must pass their own probe; there is no privileged or unconfined fallback.
- Initial npm install completed in roughly two minutes in this environment; that is an observation, not a startup deadline guarantee. No model API was called.

## Browser authentication

The [Connection implementation](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/client/connection) in the published release uses a new 32-byte base64url launch token on each process start. The web startup line carries this token. The wrapper consumes that line privately and never forwards it to Docker logs.

Exchange `GET /?token=...` with the public browser-facing `Host`. Success returns HTTP 303, `Location: ./` and a `dsh-auth-*` cookie. Keep the token exchange server-side. HTTP upstream issues a cookie without Secure; the HTTPS gateway must add Secure for the browser. Preserve HttpOnly and SameSite. Cookie authority depends on the browser-facing host/port, so never rewrite Host to `dsh:3080`.

Observed checks: unauthenticated root 401; authenticated root 200; unknown API route without a cookie 401 and with a cookie 404; authenticated `/api/remote.mux` WebSocket upgrade 101. Restart invalidates the old launch token, but an unexpired browser cookie remains valid when the DSH home and authority are unchanged. Do not read the credentials file to implement this bridge.

The gateway independently requires its own valid user session for every DSH request, including direct token-exchange routes. Possessing a DSH cookie alone must never bypass gateway login. The gateway must not replay a state-changing request after obtaining a new DSH cookie.

## Container boundary

| Setting | Contract |
| --- | --- |
| Service name / port | `dsh`, internal HTTP port `3080`; no host publication |
| HOME | `/home/dsh`, a dedicated host bind mount |
| DSH_HOME | `/home/dsh/.dsh` |
| PUBLIC_URL | Explicit public HTTPS origin with `/` path; no userinfo, query or fragment |
| DSH_VERSION | Exact bundled supported version, initially `0.2.1-rc.5` |
| DSH_HANDOFF_FILE | `/run/rdsh/connection.json` |
| DSH_UPSTREAM (gateway) | `http://dsh:3080`, configured independently of handoff data |
| Runtime user | Build arguments `RDSH_UID` / `RDSH_GID`, both default `1000` |

The handoff is UTF-8 JSON `{ "token": "...", "generation": "..." }`, no more than 4096 bytes. `generation` is a fresh UUID per DSH process. The runtime writes a temporary file and atomically renames it; only that runtime can write the volume, while the gateway mounts it read-only. The runtime removes stale handoff state before boot and removes current state on shutdown. A missing or invalid handoff fails closed. No shared HOME or gateway credentials are needed.

The wrapper selects one concrete container IPv4 address, supplies `--no-open`, `--host`, `--port`, `--public-url` and `--trusted-host`. Only the DSH network is attached to this container. Recompute the bind address on every start; DSH rejects wildcard binds.

Gateway HTTPS may terminate in the gateway's ACME mode or in an existing external reverse proxy. In external mode, the gateway-to-DSH connection and the external-proxy-to-gateway connection are HTTP on restricted deployment networks, while PUBLIC_URL remains the browser-facing HTTPS origin.

## Repeating the upstream probe

`tests/runtime/upstream-probe.mjs` runs inside an isolated Linux container with the published package installed. Set `DSH_PROBE_BIN` to that installation's `@deepseek-ai/dsh/lib/bin.js` and use an empty temporary DSH_HOME. It prints only pass/fail outcomes, keeps generated credentials in memory, and checks HTTP, WebSocket and restart behavior. It makes no model calls. Never point this test at a user's existing home.
