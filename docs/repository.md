# Repository layout

- `gateway/`: authentication, reverse proxy and optional ACME HTTPS container.
- `dsh/`: Node.js runtime image and startup installation of a pinned DSH release.
- `docs/`: deployment instructions and the tested integration contract.
- `examples/`: optional external reverse proxy configuration.
- `scripts/install.sh`: Bash bootstrap from the master archive, for new directories only.
- `tests/integration/`: checks spanning both containers.
- `plans/`: implementation plans; completed plans move to `plans/done/`.

Runtime data and credentials belong in ignored directories, never in build
contexts. Each Docker image uses its own component directory as its build context.
GitHub Actions publishes `linux/amd64` and `linux/arm64` images to
`ghcr.io/reansn0w/rdsh-gateway` and `ghcr.io/reansn0w/rdsh-dsh`. The user-facing
Compose pulls these images and contains no local build contexts.

rdsh is an independent deployment wrapper for
[DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness).
Upstream is installed from npm at runtime, not vendored in this repository.
No official logo, font or image is copied into the repository scaffold.
A license for rdsh itself has not yet been selected by its owner; do not present
it as carrying the upstream project's license or announce a public release until
that decision is made. Upstream and dependency licenses continue to apply to
their respective code and assets.
