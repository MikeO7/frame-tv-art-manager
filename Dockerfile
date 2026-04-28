# Build stage
# We use --platform=$BUILDPLATFORM to ensure the builder always runs natively
# on the GitHub Actions runner (typically amd64).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

RUN apk add --no-cache tzdata

WORKDIR /src

# Download dependencies first (layer caching).
COPY go.mod go.sum ./
RUN go mod download

# These ARG values are automatically populated by Docker Buildx.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

# Build the binary using Go's native cross-compilation.
# We map TARGETPLATFORM components to GOOS/GOARCH/GOARM.
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    $(if [ "$TARGETARCH" = "arm" ]; then echo "GOARM=${TARGETVARIANT#v}"; fi) \
    go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${GIT_COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /frame-tv-art-manager ./cmd/frame-tv-art-manager

# Runtime stage — minimal distroless image.
# The base image is automatically matched to the TARGETPLATFORM.
FROM gcr.io/distroless/static-debian12:latest

# Copy the binary.
COPY --from=builder /frame-tv-art-manager /frame-tv-art-manager

# Create default directories.
VOLUME ["/data"]

ENTRYPOINT ["/frame-tv-art-manager"]
