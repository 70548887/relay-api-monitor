# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS frontend
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci
COPY web/ ./web/
RUN npm --prefix web run build

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY VERSION ./VERSION
COPY --from=frontend /src/internal/webassets/dist/ ./internal/webassets/dist/
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN set -eu; \
    version="$(cat VERSION | tr -d '\r\n')"; \
    test -z "${VERSION:-}" || test "$VERSION" = "$version"; \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
      -ldflags "-s -w -X main.version=$version -X main.commit=$COMMIT -X main.buildTime=$BUILD_TIME" \
      -o /out/relaylab ./cmd/relaylab; \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
      -ldflags "-s -w -X main.version=$version -X main.commit=$COMMIT -X main.buildTime=$BUILD_TIME" \
      -o /out/relaylab-launcher ./cmd/relaylab-launcher

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 relaylab \
    && useradd --uid 10001 --gid 10001 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin relaylab \
    && mkdir -p /var/lib/relaylab/data /var/lib/relaylab/backups /etc/relaylab \
    && chown -R 10001:10001 /var/lib/relaylab
COPY --from=builder --chmod=0555 /out/relaylab /out/relaylab-launcher /usr/local/bin/
COPY --chmod=0444 deploy/docker/config.container.yaml /etc/relaylab/config.yaml
ARG VERSION
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.title="Relay Lab" \
      org.opencontainers.image.source="https://github.com/70548887/relay-api-monitor" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT" \
      org.opencontainers.image.created="$BUILD_TIME"
ENV RELAYLAB_CONFIG=/etc/relaylab/config.yaml \
    RELAYLAB_DATA_DIR=/var/lib/relaylab/data \
    RELAYLAB_ADDR=0.0.0.0:42783 \
    RELAYLAB_UPDATE_ENABLED=false
WORKDIR /var/lib/relaylab
USER 10001:10001
VOLUME ["/var/lib/relaylab"]
EXPOSE 42783
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget --quiet --output-document=/dev/null http://127.0.0.1:42783/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/relaylab-launcher"]
CMD ["--relaylab", "/usr/local/bin/relaylab", "--config", "/etc/relaylab/config.yaml"]
