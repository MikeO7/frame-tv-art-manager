# Build stage
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder

WORKDIR /src

# Step 1: Download dependencies.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# These ARG values are automatically populated by Docker Buildx.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

# Step 2: Build the binary.
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

# We use a robust shell script to handle GOARM only when targeting 32-bit ARM.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ "$TARGETARCH" = "arm" ]; then \
        export GOARM="${TARGETVARIANT#v}"; \
    fi; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${GIT_COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /frame-tv-art-manager ./cmd/frame-tv-art-manager

# Runtime stage — minimal distroless image.
FROM gcr.io/distroless/static-debian13:latest@sha256:448810357cc8e67b7208988abdba0f5ec3d3aebee0a5b13eb624979792cffc0f

# Copy the binary.
COPY --from=builder /frame-tv-art-manager /frame-tv-art-manager

# Artwork, tokens, and state live under /data.
VOLUME ["/data"]

# The entrypoint starts as root so it can create/chown bind-mounted data paths
# for PUID/PGID deployments. Operators that pre-own /data should set `user:`
# in Compose to run the process without root privileges.

# Report container health. Restart behavior is controlled by the runtime or orchestrator.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/frame-tv-art-manager", "-livenesscheck"]

ENTRYPOINT ["/frame-tv-art-manager"]
