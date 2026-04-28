# Build stage
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

RUN apk add --no-cache tzdata

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
FROM gcr.io/distroless/static-debian12:latest

# Copy the binary.
COPY --from=builder /frame-tv-art-manager /frame-tv-art-manager

# Create default directories.
VOLUME ["/data"]

ENTRYPOINT ["/frame-tv-art-manager"]
