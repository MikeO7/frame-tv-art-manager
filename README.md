# Frame TV Art Manager

A Go service that synchronizes artwork from a local directory to one or more Samsung Frame TVs over a secure WebSocket connection (WSS, port 8002).



## Features

- **Automatic artwork sync** — drop images in a folder, they appear on your TV
- **Multi-TV support** — sync to multiple Frame TVs simultaneously
- **HTTPS only** — secure WebSocket on port 8002 with token persistence
- **Slideshow control** — override or preserve TV's slideshow settings
- **Solar brightness** — automatic brightness based on sun position
- **Auto-off** — power off TVs at a scheduled time
- **Wake-on-LAN** — wake sleeping TVs before syncing
- **Dry-run mode** — preview all operations without touching your TV
- **Diagnostics** — built-in connection diagnostic tool
- **Tiny Docker image** — ~8MB scratch container (vs ~150MB for Python)

## Quick Start

### Docker Compose (Recommended)

1. Create your artwork directory:
   ```bash
   mkdir artwork tokens
   ```

2. Add images (`.jpg`, `.jpeg`, `.png`) to the `artwork/` folder.

3. Create `docker-compose.yml`:
   ```yaml
   services:
     frame-tv-art-manager:
       image: mikeo/frame-tv-art-manager
       restart: unless-stopped
       environment:
         TV_IPS: "192.168.1.100"
       volumes:
         - ./artwork:/artwork:ro
         - ./tokens:/tokens
   ```

4. Start:
   ```bash
   docker compose up -d
   ```

5. **First run**: The TV will show an "Allow/Deny" prompt. Press **Allow** on your TV remote. The token is saved and you won't be prompted again.

6. **View your photos on TV**: Navigate to **Art Mode → Art Store → My Photos**.

### Local Development

```bash
# Install Go 1.24+
export TV_IPS="192.168.1.100"
export ARTWORK_DIR="./artwork"
export TOKEN_DIR="./tokens"

go run ./cmd/frame-tv-art-manager
```

## Commands

```bash
# Run the sync loop (default)
frame-tv-art-manager sync

# Run with dry-run (no changes made)
frame-tv-art-manager sync --dry-run

# Run connection diagnostics
frame-tv-art-manager diagnose
```

## Configuration

All configuration is via environment variables. See [`.env.example`](.env.example) for the full reference.

### Required

| Variable | Description |
|---|---|
| `TV_IPS` | Comma-separated TV IP addresses |

### Core

| Variable | Default | Description |
|---|---|---|
| `SYNC_INTERVAL_MINUTES` | `5` | Minutes between sync cycles |
| `CLIENT_NAME` | `FrameTVArtworkSync` | WebSocket client identity |
| `MATTE_STYLE` | `none` | Artwork border style |
| `LOG_LEVEL` | `info` | Logging verbosity |
| `REMOVE_UNKNOWN_IMAGES` | `false` | Delete untracked images on TV |

### Optional Features

| Variable | Default | Description |
|---|---|---|
| `TV_MAC` | *(unset)* | MAC address for Wake-on-LAN |
| `ENABLE_REST_GATE` | `false` | Art mode probe before connecting |
| `SLIDESHOW_ENABLED` | *(unset)* | Override slideshow settings |
| `BRIGHTNESS` | *(unset)* | Fixed brightness (0–50) |
| `SOLAR_BRIGHTNESS_ENABLED` | `false` | Auto brightness from sun position |
| `AUTO_OFF_TIME` | *(unset)* | Time to power off TVs (24h format) |

## Architecture

```
cmd/frame-tv-art-manager/main.go    — CLI entry point
internal/
  config/                           — Environment variable loading
  samsung/
    client.go                       — High-level TV client facade
    connection.go                   — WSS connection + token management
    artapi.go                       — Art channel request/response
    d2d.go                          — D2D socket image transfer
    rest.go                         — REST API (device info)
    gate.go                         — Silent REST gate
    remote.go                       — Remote control (power off)
    wol.go                          — Wake-on-LAN
  sync/
    engine.go                       — Sync orchestrator
    mapping.go                      — Filename↔content_id persistence
    filescanner.go                  — Local artwork scanner
  brightness/solar.go               — Solar position + brightness calc
  schedule/autooff.go               — Auto-off time window
  sanitize/filename.go              — Filename sanitization
```

## 2024 Model Support (LS03D)

This project includes hardening for 2024 Samsung Frame TVs:

- **Port 8002 only** — ensures token persistence and stable connections
- **Unified client identity** — prevents "Samsung Remote" popup loops
- **Optional REST gate** — silent art mode detection before connecting
- **Calibrated timeouts** — tuned for 2024 panel processing overhead

## Building

```bash
# Build binary
go build -o frame-tv-art-manager ./cmd/frame-tv-art-manager

# Build Docker image
docker build -t frame-tv-art-manager .

# Run tests
go test ./...
```

## License

MIT — see [LICENSE](LICENSE).

## Credits

Samsung WebSocket protocol inspired by the [samsung-tv-ws-api](https://github.com/NickWaterton/samsung-tv-ws-api) community research.