# --- Build stage ---
# Build on the machine's native platform and cross-compile for the target,
# so `docker buildx build --platform linux/arm64` (Raspberry Pi) is fast on any host.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 → a fully static binary that runs on a distroless image.
# Note: this only works with a SQLite driver that doesn't need cgo.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/router ./cmd/router

# Empty data dir owned by the nonroot user; a fresh Docker volume copies this ownership
RUN mkdir -p /out/data

# --- Runtime stage: no shell, no package manager, runs as non-root ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/router /router
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 8080
ENTRYPOINT ["/router"]
