# 🎨 Samsung Frame TV Art Manager

[![CI Status](https://github.com/MikeO7/frame-tv-art-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/MikeO7/frame-tv-art-manager/actions)

Getting your own photos and artwork onto a Samsung Frame TV with the official SmartThings app is slow and fiddly. **Frame TV Art Manager** is a self-hosted background service that does it for you: it connects to your TV over your local network, processes your images so they look like real framed art, and keeps your gallery in sync — completely hands-free.

Point it at a folder, drop photos in from your phone, or let it auto-curate fresh artwork from Unsplash, NASA, and museum collections. It runs quietly in Docker and just works.

---

## ✨ What It Does

### 📱 Drag-and-drop Web Uploader (+ Apple Photos)
Put a photo on the TV in seconds, from any device on your network.
- **Web UI:** open `http://<server-ip>:8080/upload` in any browser and drag in your JPEGs or PNGs. There's a clean, mobile-friendly drop zone with live upload progress.
- **Apple Photos / iOS Shortcuts:** build a one-tap iOS Shortcut that POSTs your favorite photos to the same `/upload` endpoint — great for sending iPhone photos to the living room automatically. (See [Syncing Apple Photos](#-syncing-apple-photos-from-ios).)
- **Automatic de-duplication:** uploads are hashed by content, so sending the same picture twice never clutters your gallery.

> The uploader is opt-in — set `UPLOAD_ENABLED=true` to turn it on.

### 🖼️ Auto-Curate from Unsplash, NASA & Museums
Stop downloading and resizing files by hand. Give the manager a small list of sources and it pulls fresh, high-resolution art on every sync cycle:
- **Unsplash** — sync an entire collection or a single photo.
- **NASA** — wake up to the Astronomy Picture of the Day, or search the NASA image library.
- **Art Institute of Chicago** — rotate through masterpieces by searching for `monet`, `van gogh`, etc., or pull a specific artwork by ID.
- **Pexels** — search, curated picks, full collections, or a single photo.
- **Pixabay** — search, editor's choice, a specific photo, or an artist's full gallery.
- **Direct URLs** — point at any JPEG/PNG link.

Images that disappear from your source list are automatically removed from the local collection, so your gallery always matches your config.

### 🎨 "Museum Mode" & Smart Image Processing
The manager doesn't just crop — it runs a real image pipeline so a digital screen reads like physical art:
- **4K optimization:** oversized images are downscaled to crisp 4K (3840×2160) with high-quality resampling. *(On by default.)*
- **Director's Cut smart crop:** content-aware saliency analysis (edges, faces, color contrast, rule-of-thirds) finds the real subject and crops the 16:9 frame around it instead of blindly cutting the center. *(Opt-in.)*
- **Museum Mode:** layers canvas weave, impasto brush shading, gentle warming, and paper grain for a tangible, matte gallery look, with adjustable intensity. *(Opt-in.)*
- **Portrait handling:** choose how vertical phone photos fill the wide screen — `collage` (two portraits side-by-side), `pad` (blurred backdrop bars), or `crop`.
- **Mattes:** add a Frame-style border in any of Samsung's matte styles and colors (or `none` for full-screen).

### ☀️ Smart Brightness, Auto-Off & Slideshow
- **Sun-aware brightness:** give it your latitude/longitude and it dims and brightens the TV as the sun moves — or set a fixed brightness instead.
- **Nightly auto-off:** set a time (e.g. `22:00`) to power down Art Mode and save energy. TVs being used for other content (apps, HDMI) are left untouched.
- **Slideshow control:** preserve your TV's existing slideshow settings by default, or take over the shuffle/sequential timing yourself.
- **Wake-on-LAN, multiple TVs & monitoring:** wake sleeping TVs by MAC address, sync several Frames at once from one server, and expose `/health` and `/status` endpoints for Uptime Kuma, Home Assistant, or Docker health checks.

---

## 🚀 Quick Start (Docker)

### 1. Create a `docker-compose.yml`
```yaml
services:
  frame-tv-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    container_name: frame-tv-art-manager
    restart: unless-stopped
    ports:
      - "8080:8080" # Web uploader + health checks
    environment:
      TV_IPS: "192.168.1.150"        # REPLACE with your TV's IP
      CLIENT_NAME: "Home Server"
      UPLOAD_ENABLED: "true"         # Enable the drag-and-drop uploader
      SMART_CROP_ENABLED: "true"     # Subject-aware cropping
      IMAGE_MUSEUM_MODE: "true"      # Canvas/impasto "real art" look
    volumes:
      # One mount is enough — the app creates artwork/ and tokens/ inside it.
      - ./data:/data
```

### 2. Spin it up
```bash
docker compose up -d
```

### 3. Authenticate (one time)
With `UPLOAD_ENABLED=true`, open `http://<server-ip>:8080/upload` and upload an image. Look at your TV — a prompt will ask you to authorize **"Home Server"**. Press **Allow** on the remote. The token is saved to `./data/tokens`, and the service runs silently from then on.

> Image optimization is on by default. Set `IMAGE_OPTIMIZE_ENABLED=false` to upload images untouched.

---

## 🌍 Auto-Curating from Unsplash & Feeds

Tell the manager where your sources config lives, then list what you want.

### docker-compose.yml
```yaml
    environment:
      ARTWORK_SOURCES_FILE: "/data/sources.yaml"
      UNSPLASH_ACCESS_KEY: "your_unsplash_access_key"   # only the Access Key is needed
      PEXELS_API_KEY: "your_pexels_key"                 # optional
      PIXABAY_API_KEY: "your_pixabay_key"               # optional
```

### sources.yaml
Group sources under a `providers:` map. Each entry is a `command` for that provider:

```yaml
providers:
  # 📸 Unsplash — needs UNSPLASH_ACCESS_KEY
  unsplash:
    - "collection:225444"     # every photo in a collection
    - "photo:L9W_5q57_V8"     # one specific photo

  # 🚀 NASA — works with the built-in DEMO_KEY (set NASA_API_KEY for higher limits)
  nasa:
    - "apod"                  # today's Astronomy Picture of the Day
    - "search:james webb"     # top results from the NASA image library

  # 🎨 Art Institute of Chicago — no key required
  art_institute_of_chicago:
    - "search:monet"          # masterpieces matching a query
    - "photo:20684"           # a specific artwork by ID

  # 🌿 Pexels — needs PEXELS_API_KEY
  pexels:
    - "search:nature"
    - "curated"

  # 🍃 Pixabay — needs PIXABAY_API_KEY
  pixabay:
    - "search:mountains"
    - "editors_choice"

  # 🔗 Any direct image link
  direct:
    - "https://example.com/artwork.jpg"
```

If the file doesn't exist yet, the app writes a fully-commented starter `sources.yaml` for you on first run. A plain `.txt` file (one source per line, `#` for comments) also works.

*Get free Unsplash keys at [unsplash.com/developers](https://unsplash.com/developers), Pexels at [pexels.com/api](https://www.pexels.com/api/), and Pixabay at [pixabay.com/api/docs](https://pixabay.com/api/docs/).*

---

## 📱 Syncing Apple Photos from iOS

Use the built-in **Shortcuts** app to send photos to your TV in one tap.

1. Make sure `UPLOAD_ENABLED=true` and port `8080` is mapped.
2. In **Shortcuts**, create a new shortcut.
3. Add **Find Photos** → filter by `Favorite is Yes` and `Media Type is Image` (limit to a sensible count).
4. Add **Repeat with Each**.
5. Inside the loop, add **Convert Image** → `JPEG` (Frame TVs don't support HEIC).
6. Inside the loop, add **Get Contents of URL**:
   - URL: `http://<server-ip>:8080/upload`
   - Method: `POST`
   - Request Body: `Form`, with a field named **`file`** set to the converted image.
7. Run it — or attach it to an iOS **Automation** to push photos automatically (e.g. nightly).

Duplicate photos are detected automatically, so re-running the shortcut is safe.

---

## ⚙️ Configuration

Everything is configured through environment variables. **Only `TV_IPS` is required.**

### Core
| Variable | Default | Description |
| :--- | :--- | :--- |
| `TV_IPS` | *required* | TV IP address(es), comma-separated for multiple Frames. |
| `CLIENT_NAME` | `Frame Art Manager` | Connection name shown by the TV. A stable name avoids repeat Allow/Deny prompts. |
| `SYNC_INTERVAL_MINUTES` | `5` | Minutes between sync cycles. |
| `MATTE_STYLE` | `none` | Frame border as `{style}_{color}` (e.g. `shadowbox_polar`). Styles: `modernthin`, `modern`, `modernwide`, `flexible`, `shadowbox`, `panoramic`, `triptych`, `mix`, `squares`. |
| `REMOVE_UNKNOWN_IMAGES` | `false` | Delete images on the TV that this service doesn't manage. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |

### Web Uploader & Sources
| Variable | Default | Description |
| :--- | :--- | :--- |
| `UPLOAD_ENABLED` | `false` | Enable the drag-and-drop uploader and `POST /upload` on port 8080. |
| `ARTWORK_SOURCES_FILE` | *(empty)* | Path to your sources list (e.g. `/data/sources.yaml`). |
| `UNSPLASH_ACCESS_KEY` | *(empty)* | Unsplash API Access Key. |
| `PEXELS_API_KEY` | *(empty)* | Pexels API key. |
| `PIXABAY_API_KEY` | *(empty)* | Pixabay API key. |
| `NASA_API_KEY` | `DEMO_KEY` | NASA API key (the demo key works, with lower rate limits). |
| `MAX_ARTWORK_IMAGES` | `0` | Cap on synced images (`0` = fill to the TV's storage limit). |
| `MAX_DOWNLOAD_SIZE_MB` | `20` | Max size per downloaded/uploaded image. |

### Image Processing
| Variable | Default | Description |
| :--- | :--- | :--- |
| `IMAGE_OPTIMIZE_ENABLED` | `true` | Downscale oversized images to crisp 4K. |
| `IMAGE_MAX_WIDTH` / `IMAGE_MAX_HEIGHT` | `3840` / `2160` | Target resolution. |
| `IMAGE_JPEG_QUALITY` | `95` | JPEG encode quality (1–100). |
| `SMART_CROP_ENABLED` | `false` | Subject-aware "Director's Cut" cropping. |
| `IMAGE_MUSEUM_MODE` | `false` | Canvas texture, impasto, warming, grain. |
| `IMAGE_MUSEUM_INTENSITY` | `5` | Strength of Museum Mode (1–10). |
| `PORTRAIT_MODE` | `crop` | Vertical photos: `collage`, `pad`, or `crop`. |

### Smart Home & Automation
| Variable | Default | Description |
| :--- | :--- | :--- |
| `BRIGHTNESS` | *(empty)* | Fixed brightness (0–50), applied each cycle. |
| `SOLAR_BRIGHTNESS_ENABLED` | `false` | Auto-adjust brightness by sun elevation (takes precedence over `BRIGHTNESS`). |
| `LOCATION_LATITUDE` / `LOCATION_LONGITUDE` | *(empty)* | Required when solar brightness is on. |
| `LOCATION_TIMEZONE` | `UTC` | IANA timezone (e.g. `America/New_York`). Required for auto-off. |
| `BRIGHTNESS_MIN` / `BRIGHTNESS_MAX` | `2` / `10` | Brightness range for solar mode. |
| `AUTO_OFF_TIME` | *(empty)* | Power off Art Mode at this 24h time (e.g. `22:00`). |
| `AUTO_OFF_GRACE_HOURS` | `2` | How long after `AUTO_OFF_TIME` to keep trying. |
| `SLIDESHOW_ENABLED` / `SLIDESHOW_INTERVAL` / `SLIDESHOW_TYPE` | *(unset)* | Override slideshow (`shuffle`/`sequential`). If unset, the TV's own settings are preserved. |
| `TV_MAC` | *(empty)* | MAC address for Wake-on-LAN. |

### System
| Variable | Default | Description |
| :--- | :--- | :--- |
| `HEALTH_PORT` | `8080` | Port for `/health`, `/status`, and `/upload`. `0` disables the server. |
| `ENABLE_REST_GATE` | `false` | Probe port 8001 to skip TVs busy with other content (firmware-dependent). |
| `PUID` / `PGID` | `0` | Owner UID/GID for created data directories. |
| `DRY_RUN` | `false` | Process images locally without touching the TV. |

A full annotated list lives in [`.env.example`](.env.example).

---

## 📈 Monitoring

When the health server is running, two JSON endpoints are available:
- `GET /health` — uptime, last sync result, current stage.
- `GET /status` — the above plus per-TV details (art mode, image count, reachability).

The Docker image also ships a built-in `-healthcheck` command used by the container `HEALTHCHECK`, so orchestrators can self-heal automatically.

---

## 🛠️ Development

Built with Go (see [`go.mod`](go.mod)).

```bash
make build   # Build the binary
make test    # Run the test suite with coverage
make check   # Tests, linters, vuln scan, formatting — the full pipeline
make fix     # Auto-format and auto-fix lint issues
make docker  # Build the Docker image locally
```

## 📄 License
Licensed under the **PolyForm Noncommercial License 1.0.0**. Run it at home and modify it freely; commercial use requires separate licensing.
