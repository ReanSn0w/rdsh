FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rdsh-gateway . \
    && mkdir -p /out/state /out/handoff \
    && chmod 0700 /out/state /out/handoff

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG RDSH_UID=1000
ARG RDSH_GID=1000
COPY --from=build /out/rdsh-gateway /usr/local/bin/rdsh-gateway
COPY --from=build --chown=${RDSH_UID}:${RDSH_GID} /out/state /var/lib/rdsh
COPY --from=build --chown=${RDSH_UID}:${RDSH_GID} /out/handoff /run/rdsh
USER ${RDSH_UID}:${RDSH_GID}
EXPOSE 8080 8443
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/rdsh-gateway", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/rdsh-gateway"]
