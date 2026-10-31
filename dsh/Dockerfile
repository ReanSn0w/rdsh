FROM node:24.22.1-bookworm-slim@sha256:be8204d4bd327363357f532187b752ff62759c31bedf98e9e18fc08c9cbd605c

ARG RDSH_UID=1000
ARG RDSH_GID=1000
RUN test "$RDSH_UID" -gt 0 && test "$RDSH_GID" -gt 0 \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git tini util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && COREPACK_HOME=/opt/corepack corepack enable pnpm \
    && COREPACK_HOME=/opt/corepack corepack install --global pnpm@11.28.5 \
    && COREPACK_HOME=/opt/corepack pnpm --version | grep -Fx 11.28.5 \
    && userdel node \
    && if getent group node >/dev/null; then groupdel node; fi \
    && if ! getent group "$RDSH_GID" >/dev/null; then groupadd --gid "$RDSH_GID" dsh; fi \
    && useradd --uid "$RDSH_UID" --gid "$RDSH_GID" --home-dir /home/dsh --shell /bin/bash --create-home dsh \
    && install -d -m 0700 -o "$RDSH_UID" -g "$RDSH_GID" /run/rdsh

COPY --chmod=0555 *.mjs with-home-lock.sh /opt/rdsh/
COPY releases/ /opt/rdsh/releases/

ENV HOME=/home/dsh \
    DSH_HOME=/home/dsh/.dsh \
    DSH_VERSION=0.2.1-rc.5 \
    DSH_HANDOFF_FILE=/run/rdsh/connection.json \
    NARB_NATIVE_CACHE_DIR=/home/dsh/.cache/native \
    npm_config_cache=/home/dsh/.cache/npm \
    COREPACK_HOME=/opt/corepack \
    COREPACK_DEFAULT_TO_LATEST=0 \
    PATH=/home/dsh/.rdsh/current/node_modules/.bin:$PATH

WORKDIR /home/dsh
USER ${RDSH_UID}:${RDSH_GID}
EXPOSE 3080
HEALTHCHECK --interval=10s --timeout=5s --start-period=15m --retries=3 CMD ["node", "/opt/rdsh/healthcheck.mjs"]
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/opt/rdsh/with-home-lock.sh"]
CMD ["node", "/opt/rdsh/runtime.mjs"]
