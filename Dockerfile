# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Download dependencies first (layer caching).
COPY go.mod go.sum ./
RUN go mod download

# Build the binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /frame-tv-art-manager ./cmd/frame-tv-art-manager

# Runtime stage — minimal scratch image.
FROM scratch

# Copy CA certificates for HTTPS (TV uses self-signed, but we need the
# root CAs for potential future use and for time zone data).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data for solar brightness calculations.
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary.
COPY --from=builder /frame-tv-art-manager /frame-tv-art-manager

# Create default directories.
# (These will be overridden by volume mounts in docker-compose.)
VOLUME ["/artwork", "/tokens"]

ENTRYPOINT ["/frame-tv-art-manager"]
CMD ["sync"]
