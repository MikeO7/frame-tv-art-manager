# Frame TV Art Manager

A Go service that keeps a folder of images in sync with your Samsung Frame TV. Point it at a directory of JPEGs and PNGs, give it your TV's IP address, and it handles the rest — uploading new images, removing deleted ones, and optionally pulling artwork from places like Unsplash, NASA, and the Art Institute of Chicago.

I built this because the existing tools kept breaking on my 2024 Frame TV (LS03D / Tizen 8.0). The newer firmware changed how the WebSocket protocol works, and most libraries haven't caught up. This one has specific handling for Tizen 8.0+ quirks while staying backward-compatible with older models.

---

## Quick Start

### Docker Compose (Recommended)

Create a `docker-compose.yml`:

```yaml
services:
  frame-tv-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    restart: unless-stopped
    environment:
      TV_IPS: "192.168.1.100"
    volumes:
      - ./data:/data
```

That's the bare minimum. Start it with `docker compose up -d` and drop some `.jpg` or `.png` files into `./data/artwork/`. The manager creates the `artwork` and `tokens` subdirectories automatically on first run.

On the **very first connection**, the TV will show an "Allow/Deny" prompt. Hit **Allow** with your remote. The manager saves the token and won't ask again.

### A More Complete Example

Here's what a real setup might look like with common options enabled:

```yaml
services:
  frame-tv-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    restart: unless-stopped
    environment:
      # Required
      TV_IPS: "192.168.1.100"

      # Sync every 15 minutes instead of the default 5
      SYNC_INTERVAL_MINUTES: "15"

      # Resize oversized images to 4K and smart-crop to 16:9
      IMAGE_OPTIMIZE_ENABLED: "true"
      SMART_CROP_ENABLED: "true"

      # Shuffle through images every hour
      SLIDESHOW_ENABLED: "true"
      SLIDESHOW_INTERVAL: "60"
      SLIDESHOW_TYPE: "shuffle"

      # Adjust brightness based on sun position
      SOLAR_BRIGHTNESS_ENABLED: "true"
      LOCATION_LATITUDE: "39.7392"
      LOCATION_LONGITUDE: "-104.9903"
      LOCATION_TIMEZONE: "America/Denver"
      BRIGHTNESS_MIN: "2"
      BRIGHTNESS_MAX: "10"

      # Turn the TV off at 11pm if it's still in art mode
      AUTO_OFF_TIME: "23:00"
      AUTO_OFF_GRACE_HOURS: "2"

      # Pull art from a sources file
      ARTWORK_SOURCES_FILE: "/data/sources.yaml"

      # Timezone for container logs
      TZ: "America/Denver"
    volumes:
      - ./data:/data
```

### Multiple TVs

Just list all IPs separated by commas:

```yaml
environment:
  TV_IPS: "192.168.1.100,192.168.1.101,192.168.1.102"
```

Each TV gets its own token, mapping file, and metadata file. They all sync from the same artwork folder.

### Running Without Docker

Release binaries are built for Linux (amd64, arm64, armv7) and macOS (amd64, arm64). Grab one from the [releases page](https://github.com/MikeO7/frame-tv-art-manager/releases), then:

```bash
export TV_IPS=192.168.1.100
export ARTWORK_DIR=./my-artwork
export TOKEN_DIR=./tokens
./frame-tv-art-manager
```

Or build from source:

```bash
go build -o frame-tv-art-manager ./cmd/frame-tv-art-manager
```

---

## How a Sync Cycle Works

Every cycle (default: 5 minutes), the manager runs through this sequence:

1. **Download sources** — If you've configured a `sources.yaml`, new images are fetched from APIs
2. **Validate images** — Every file is decoded to catch corrupt or unsupported files before they reach the TV
3. **Optimize** — Oversized JPEGs are resized to 4K and/or cropped to 16:9 (configurable)
4. **Check TV** — Optionally probes the TV via REST to see if it's in Art Mode (avoids popups while watching Netflix)
5. **Connect** — Opens an encrypted WebSocket (WSS) connection on port 8002
6. **Diff** — Compares your local folder against the TV's inventory using a persistent `mapping.json`
7. **Upload/Delete** — Pushes new images, removes deleted ones
8. **Slideshow** — Applies slideshow settings, or preserves the TV's current ones
9. **Brightness** — Sets brightness (fixed or solar-calculated)
10. **Auto-off** — If within the auto-off window and the TV is in Art Mode, powers it off

If a TV is unreachable, the manager uses exponential backoff (capped at 1 hour) instead of retrying every cycle.

---

## Configuration Reference

Everything is configured through environment variables. Only `TV_IPS` is required — everything else has sensible defaults.

### Core

| Variable | Default | Description |
|---|---|---|
| `TV_IPS` | *(required)* | Comma-separated TV IP addresses |
| `ARTWORK_DIR` | `/data/artwork` | Path to your images folder |
| `TOKEN_DIR` | `/data/tokens` | Path for auth tokens, mappings, and metadata |
| `SYNC_INTERVAL_MINUTES` | `5` | Minutes between sync cycles |
| `CLIENT_NAME` | `Frame Art Manager` | Name shown on the TV during authorization |
| `LOG_LEVEL` | `info` | Logging verbosity: `debug`, `info`, `warn`, `error` |
| `DRY_RUN` | `false` | Log what would happen without actually doing it |

### Image Processing

| Variable | Default | Description |
|---|---|---|
| `IMAGE_OPTIMIZE_ENABLED` | `true` | Resize oversized JPEGs to fit within max dimensions |
| `SMART_CROP_ENABLED` | `true` | Crop images to 16:9 using entropy-based analysis |
| `IMAGE_MAX_WIDTH` | `3840` | Maximum width in pixels |
| `IMAGE_MAX_HEIGHT` | `2160` | Maximum height in pixels |
| `IMAGE_JPEG_QUALITY` | `92` | JPEG quality for re-encoded images (1–100) |

Smart crop uses the [smartcrop](https://github.com/muesli/smartcrop) library to find the most visually interesting region of the image, then scales the cropped result to exactly 3840×2160 using CatmullRom resampling. PNG files are skipped (they may need transparency for matte effects). Even with optimization disabled, every image is still validated by decoding it — corrupt files are logged and excluded.

### Matte / Border Style

| Variable | Default | Description |
|---|---|---|
| `MATTE_STYLE` | `none` | Default border style for uploaded artwork |

The format is `{style}_{color}`, or `none` for full-screen display.

**Available styles:** `modernthin`, `modern`, `modernwide`, `flexible`, `shadowbox`, `panoramic`, `triptych`, `mix`, `squares`

**Available colors:** `black`, `neutral`, `antique`, `warm`, `polar`, `sand`, `seafoam`, `sage`, `burgandy`, `navy`, `apricot`, `byzantine`, `lavender`, `redorange`, `skyblue`, `turquoise`

**Examples:** `shadowbox_polar`, `modern_apricot`, `modernthin_black`, `none`

You can also set per-image overrides — see [Per-Image Matte Overrides](#per-image-matte-overrides) below.

### Slideshow

| Variable | Default | Description |
|---|---|---|
| `SLIDESHOW_ENABLED` | `false` | Override the TV's slideshow settings |
| `SLIDESHOW_INTERVAL` | `15` | Minutes between slide transitions |
| `SLIDESHOW_TYPE` | `shuffle` | `shuffle` or `sequential` |

If you **don't set any** of these three variables, the manager preserves the TV's current slideshow settings. As soon as you set any one of them, the manager takes control of slideshow behavior.

Example — rotate through images randomly every 30 minutes:
```yaml
SLIDESHOW_ENABLED: "true"
SLIDESHOW_INTERVAL: "30"
SLIDESHOW_TYPE: "shuffle"
```

### Brightness

Choose one: a fixed value, or automatic solar-based brightness.

#### Option 1: Fixed Brightness

```yaml
BRIGHTNESS: "5"
```

#### Option 2: Solar Brightness

```yaml
SOLAR_BRIGHTNESS_ENABLED: "true"
LOCATION_LATITUDE: "39.7392"
LOCATION_LONGITUDE: "-104.9903"
LOCATION_TIMEZONE: "America/Denver"
BRIGHTNESS_MIN: "2"
BRIGHTNESS_MAX: "10"
```

| Variable | Default | Description |
|---|---|---|
| `BRIGHTNESS` | *(unset)* | Fixed brightness value (applied every cycle) |
| `SOLAR_BRIGHTNESS_ENABLED` | `false` | Adjust brightness based on sun elevation |
| `LOCATION_LATITUDE` | *(unset)* | Your latitude (required for solar) |
| `LOCATION_LONGITUDE` | *(unset)* | Your longitude (required for solar) |
| `LOCATION_TIMEZONE` | `UTC` | IANA timezone string |
| `BRIGHTNESS_MIN` | `2` | Brightness when the sun is below the horizon |
| `BRIGHTNESS_MAX` | `10` | Brightness when the sun is at its highest |

If both are set, solar takes priority. The solar calculation uses a proper astronomical algorithm (Julian date, solar declination, hour angle) with the Kasten-Young atmospheric attenuation model — it's not just a time-of-day lookup.

### Auto-Off

Automatically power off TVs that are in Art Mode at a set time. TVs being used for other content (HDMI, apps, etc.) are left alone.

```yaml
AUTO_OFF_TIME: "22:00"
AUTO_OFF_GRACE_HOURS: "2"
LOCATION_TIMEZONE: "America/Denver"
```

| Variable | Default | Description |
|---|---|---|
| `AUTO_OFF_TIME` | *(unset)* | Time to turn off (24-hour format, e.g. `22:00`) |
| `AUTO_OFF_GRACE_HOURS` | `2` | Hours after `AUTO_OFF_TIME` to keep trying |

Requires `LOCATION_TIMEZONE` to be set. The grace window handles cases where the sync cycle doesn't land exactly on the off time.

### Wake-on-LAN

If your TV goes into deep sleep and stops responding to WebSocket connections, you can wake it up before each sync:

```yaml
TV_MAC: "AA:BB:CC:DD:EE:FF"
```

| Variable | Default | Description |
|---|---|---|
| `TV_MAC` | *(unset)* | MAC address for Wake-on-LAN magic packet |

### REST Gate

The REST gate silently checks if the TV is in Art Mode before opening a WebSocket connection. This prevents the "Samsung Remote" popup when someone is watching Netflix or YouTube.

```yaml
ENABLE_REST_GATE: "true"
```

| Variable | Default | Description |
|---|---|---|
| `ENABLE_REST_GATE` | `false` | Probe `http://<ip>:8001/ms/art` before WSS |

**Note:** Not all firmware versions support this endpoint. Some 2024 models return 404 regardless of state. If it doesn't work for your TV, leave it disabled.

### Cleanup

| Variable | Default | Description |
|---|---|---|
| `REMOVE_UNKNOWN_IMAGES` | `false` | Delete images on the TV that aren't tracked by the manager |

When enabled, any image on the TV that doesn't appear in the manager's `mapping.json` will be removed. This is useful if you want the manager to be the single source of truth for what's on the TV.

### Health Server

| Variable | Default | Description |
|---|---|---|
| `HEALTH_PORT` | `0` (disabled) | HTTP port for health check endpoints |

```yaml
HEALTH_PORT: "8080"
```

Exposes two endpoints:
- `GET /health` — returns uptime, last sync time, sync count, and whether the last sync succeeded
- `GET /status` — returns detailed per-TV info (art mode state, image count, reachability status)

Useful for Docker healthchecks, Uptime Kuma, or Home Assistant monitoring.

### Timeouts and Retries

These rarely need changing, but they're available if your network is slow or your TV takes a while to respond:

| Variable | Default | Description |
|---|---|---|
| `CONNECTION_TIMEOUT_SECONDS` | `60` | Max seconds to wait for the WSS handshake |
| `API_TIMEOUT_SECONDS` | `60` | Max seconds to wait for art API responses |
| `UPLOAD_DELAY_MS` | `3000` | Milliseconds to pause between consecutive uploads |
| `UPLOAD_ATTEMPTS` | `3` | How many times to retry a failed upload |
| `GATE_TIMEOUT_MS` | `10000` | HTTP timeout for the REST gate probe |

### Docker Ownership

| Variable | Default | Description |
|---|---|---|
| `PUID` | `0` | User ID for created directories |
| `PGID` | `0` | Group ID for created directories |

If the artwork/token directories are mounted from the host and you're getting permission errors, set these to match your host user (commonly `1000` / `1000`).

---

## Image Sources

The manager can automatically download art from several free APIs. Create a `sources.yaml` file and point to it:

```yaml
# docker-compose.yml
environment:
  ARTWORK_SOURCES_FILE: "/data/sources.yaml"
```

### sources.yaml Format

The simplest approach is a `providers` map, grouped by service:

```yaml
providers:
  # NASA — space photography
  nasa:
    - apod                       # Today's Astronomy Picture of the Day
    - search:nebula              # Top 10 results for "nebula"
    - search:galaxy              # Top 10 results for "galaxy"

  # Art Institute of Chicago — fine art
  art_institute_of_chicago:
    - search:monet               # 10 masterpieces matching "monet"
    - search:impressionism       # 10 Impressionist paintings
    - photo:27992                # "A Sunday on La Grande Jatte" by Seurat

  # Unsplash — high-quality photography (requires API key)
  unsplash:
    - collection:225444          # All photos from a collection (up to 50)
    - photo:L9W_5q57_V8          # A specific photo by its Unsplash ID

  # Pexels — free stock photography (requires API key)
  pexels:
    - search:nature              # 10 top results for "nature"
    - curated                    # 10 hand-picked photos from Pexels
    - photo:12345                # A specific photo by its Pexels ID

  # Pixabay — free stock photography (requires API key)
  pixabay:
    - search:landscape           # 10 top results for "landscape"
    - editors_choice             # 10 Editor's Choice photos
    - user:12345                 # Up to 50 photos from a specific user
    - photo:12345                # A specific photo by its Pixabay ID

  # Direct URLs — any JPEG or PNG
  direct:
    - https://example.com/my-art.jpg
    - https://example.com/wallpaper.png
```

You can also use a flat list format:

```yaml
sources:
  - nasa:apod
  - unsplash:collection:225444
  - https://example.com/art.jpg
```

Or a plain text file (`sources.txt`, one source per line):

```
# Lines starting with # are comments
nasa:apod
unsplash:photo:L9W_5q57_V8
https://example.com/art.jpg
```

### Source Command Reference

| Provider | Command | What it pulls |
|---|---|---|
| NASA | `nasa:apod` | Today's Astronomy Picture of the Day |
| NASA | `nasa:search:nebula` | Top 10 results for a keyword |
| Art Institute | `art_institute_of_chicago:search:monet` | 10 artworks matching a keyword |
| Art Institute | `art_institute_of_chicago:photo:12345` | A specific artwork by its ID |
| Unsplash | `unsplash:collection:225444` | Up to 50 photos from a collection |
| Unsplash | `unsplash:photo:L9W_5q57_V8` | A specific photo by ID |
| Pexels | `pexels:search:nature` | 10 photos matching a keyword |
| Pexels | `pexels:curated` | 10 hand-picked photos |
| Pexels | `pexels:photo:12345` | A specific photo by ID |
| Pixabay | `pixabay:search:landscape` | 10 photos matching a keyword |
| Pixabay | `pixabay:editors_choice` | 10 Editor's Choice photos |
| Pixabay | `pixabay:user:12345` | Up to 50 photos from a user |
| Pixabay | `pixabay:photo:12345` | A specific photo by ID |
| Direct | `https://example.com/art.jpg` | Any direct URL to a JPEG or PNG |

### API Keys

| Variable | Default | Required for |
|---|---|---|
| `UNSPLASH_ACCESS_KEY` | *(unset)* | Unsplash — [get one here](https://unsplash.com/developers) |
| `PEXELS_API_KEY` | *(unset)* | Pexels — [get one here](https://www.pexels.com/api/) |
| `PIXABAY_API_KEY` | *(unset)* | Pixabay — [get one here](https://pixabay.com/api/docs/) |
| `NASA_API_KEY` | `DEMO_KEY` | NASA (optional, demo key works but has rate limits) |

The Art Institute of Chicago API doesn't require a key.

The Unsplash integration automatically tracks downloads to comply with their API terms of service.

### How Downloads Work

- Files are named by a SHA-256 hash of their source URL, so re-running the sync is safe — it only downloads images it hasn't seen before
- Downloads write to a temp file first, then rename into place (atomic write)
- If you remove a source from `sources.yaml`, the downloaded image stays in your artwork folder until you manually delete it

### Finding IDs

Just look at the URL in your browser:

| Provider | Example URL | ID to use |
|---|---|---|
| Unsplash photo | `unsplash.com/photos/L9W_5q57_V8` | `L9W_5q57_V8` |
| Unsplash collection | `unsplash.com/collections/225444/nature` | `225444` |
| Art Institute | `artic.edu/artworks/27992/a-sunday-on-la-grande-jatte` | `27992` |
| Pexels | `pexels.com/photo/landscape-12345` | `12345` |
| Pixabay photo | `pixabay.com/photos/landscape-12345/` | `12345` |
| Pixabay user | `pixabay.com/users/username-12345/` | `12345` |

---

## Per-Image Matte Overrides

If you want different border styles on different images, create a `mattes.json` file in your artwork directory:

```json
{
  "sunset.jpg": "shadowbox_polar",
  "portrait.jpg": "modern_apricot",
  "family-photo.jpg": "modernthin_black",
  "_default": "none"
}
```

Priority order:
1. Per-file entry in `mattes.json`
2. `_default` in `mattes.json`
3. Global `MATTE_STYLE` env var

---

## 2024+ Model Support (Tizen 8.0+)

This is the main reason the project exists. Samsung's 2024 firmware (Tizen 8.0) introduced several breaking changes:

- **Event name format**: Newer models use `d2d_service_message` (underscores) instead of the older `d2d.service.message.event` (dots). The manager checks for both.
- **String-wrapped JSON**: Some firmware versions send the WebSocket `data` field as a JSON-encoded string instead of a raw JSON object. The manager detects this and unwraps it.
- **Token handshake**: On 2024 models, connecting to the `com.samsung.art-app` endpoint doesn't return an auth token. The manager detects a missing token file and performs a one-time handshake through the `samsung.remote.control` endpoint to obtain one.
- **WSS only**: All connections use encrypted WebSocket (port 8002). The TV uses a self-signed certificate.

---

## Reliability

Things that make this safe to leave running indefinitely:

- **Exponential backoff** — If a TV is unreachable (Wi-Fi blip, deep sleep, etc.), the manager waits progressively longer before retrying: 5 min → 10 min → 20 min → ... up to 1 hour max. It resets on the next successful connection.
- **Image validation** — Every image is decoded before upload. Corrupt or unsupported files are logged and skipped, so you won't get stuck in a "bad upload" loop.
- **Atomic writes** — Downloaded and optimized images are written to a temp file first, then renamed. A crash mid-write won't leave you with a half-written file.
- **Graceful shutdown** — Listens for SIGINT and SIGTERM. Finishes the current sync cycle and closes all WebSocket connections before exiting.
- **Context propagation** — All operations respect cancellation, so shutdown is responsive even during uploads.

---

## Files on Disk

```
/data/
├── artwork/                                # Your images (.jpg, .jpeg, .png)
│   └── mattes.json                         # Optional per-image matte overrides
├── tokens/
│   ├── tv_192_168_1_100.txt                # Auth token for this TV
│   ├── tv_192_168_1_100_mapping.json       # Filename → content_id map
│   └── tv_192_168_1_100_metadata.json      # Device info, categories, slideshow
└── sources.yaml                            # Optional image source definitions
```

- **Token file** (`tv_<ip>.txt`) — The auth token saved after the first Allow/Deny prompt. Delete this file to force re-authorization.
- **Mapping file** (`tv_<ip>_mapping.json`) — Tracks which local filename maps to which `content_id` on the TV. This is how the manager knows what needs uploading or deleting on the next cycle.
- **Metadata file** (`tv_<ip>_metadata.json`) — Refreshed every cycle. Contains device info (model name, firmware version), slideshow status, and the TV's artwork category list. Useful for debugging.

---

## License

MIT