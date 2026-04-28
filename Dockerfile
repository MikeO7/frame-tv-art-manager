# Build stage
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

RUN apk add --no-cache tzdata

WORKDIR /src

# Step 1: Download dependencies.
# We use a cache mount to keep the Go module cache persistent between builds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# These ARG values are automatically populated by Docker Buildx.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

# Step 2: Build the binary.
# We use TWO cache mounts here:
# 1. For the Go module cache (/go/pkg/mod)
# 2. For the Go build cache (/root/.cache/go-build) - This makes incremental builds INSTANT.
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    $(if [ "$TARGETARCH" = "arm" ]; then echo "GOARM=${TARGETVARIANT#v}"; fi) \
    go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${GIT_COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /frame-tv-art-manager ./cmd/frame-tv-art-manager

# Runtime stage — minimal distroless image.
FROM gcr.io/distroless/static-debian12:latest

# Copy the binary.
COPY --from=builder /frame-tv-art-manager /frame-tv-art-manager

# Create default directories.
VOLUME ["/data"]

ENTRYPOINT ["/frame-tv-art-manager"]
