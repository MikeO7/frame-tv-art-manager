# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache tzdata

WORKDIR /src

# Download dependencies first (layer caching).
COPY go.mod go.sum ./
RUN go mod download

# Build the binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /frame-tv-art-manager ./cmd/frame-tv-art-manager

# Runtime stage — minimal distroless image.
# gcr.io/distroless/static is specifically for statically linked Go binaries.
# It contains CA certificates and timezone data.
FROM gcr.io/distroless/static-debian12:latest

# Copy the binary.
COPY --from=builder /frame-tv-art-manager /frame-tv-art-manager

# Create default directories.
# (These will be overridden by volume mounts in docker-compose.)
# Note: Distroless doesn't have a shell, but it handles VOLUME and ENTRYPOINT.
VOLUME ["/data"]

ENTRYPOINT ["/frame-tv-art-manager"]
