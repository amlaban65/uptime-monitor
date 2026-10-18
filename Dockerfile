# syntax=docker/dockerfile:1

# Build stage: full Go toolchain, discarded after the build.
FROM golang:1.24-bookworm AS build
WORKDIR /src

# Download modules in their own layer so code changes do not refetch them.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# CGO off produces a static binary that runs on a distroless/static base.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/uptime-monitor ./cmd/uptime-monitor

# Runtime stage: no shell or package manager, CA certificates and tzdata
# included, runs as an unprivileged user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/uptime-monitor /uptime-monitor
USER nonroot:nonroot
EXPOSE 8080
# No curl in the image, so the binary checks itself.
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/uptime-monitor", "healthcheck"]
ENTRYPOINT ["/uptime-monitor"]
