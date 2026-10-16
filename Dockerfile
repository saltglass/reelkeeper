# Reelkeeper container image.
#
# Both build stages run on the build machine's own architecture
# (--platform=$BUILDPLATFORM) and Go cross-compiles for the target, so
# amd64 and arm64 images build quickly without emulation.
#
# The final image has only the static binary: no shell, no package manager,
# no curl. The healthcheck uses `reelkeeper healthcheck`.

# --- Web UI ---------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY api/openapi.yaml /src/api/openapi.yaml
# Vite writes into ../internal/ui/dist (see web/vite.config.ts).
RUN mkdir -p /src/internal/ui && npm run build

# --- Go binary ------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/ui/dist ./internal/ui/dist
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# CGO off: a fully static binary. GOAMD64 stays at the default (v1), so it
# runs on CPUs without AVX2 such as some virtual machines.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X github.com/saltglass/reelkeeper/internal/buildinfo.Version=${VERSION}" \
      -o /out/reelkeeper ./cmd/reelkeeper

# --- Runtime --------------------------------------------------------------
# distroless/static: CA certificates and time zones, nothing else. Runs as
# an unprivileged user by default; compose can override it (user: "997:997").
FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="Reelkeeper" \
      org.opencontainers.image.description="Finds, downloads and organizes movies and TV. Replaces Radarr, Sonarr and Prowlarr." \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/reelkeeper /reelkeeper
ENV REELKEEPER_DATA_DIR=/config \
    REELKEEPER_PORT=7676
VOLUME /config
EXPOSE 7676
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/reelkeeper", "healthcheck"]
ENTRYPOINT ["/reelkeeper"]
CMD ["serve"]
