# Frame TV Art Manager

A robust Go service designed for "set-and-forget" artwork synchronization for Samsung Frame TVs. Specifically hardened to handle the stricter security and protocol changes in 2024+ models (LS03D / Tizen 8.0).

## 🌟 Features

- **Automated Synchronization**: Matches a local folder of images to your TV's "My Photos" collection.
- **2024 Model Hardening**: Protocol-aware handling for the latest Samsung firmware (Tizen 8.0 / Y2025 platform).
- **Smart Handshake**: Persistent token management that eliminates "Allow/Deny" popup loops.
- **Solar Brightness**: Automatically adjusts TV brightness based on the sun's elevation at your coordinates.
- **Silent Operation**: Uses a REST-based "Gate" to check TV status before initiating WebSocket connections.
- **Smart Cropping**: Uses entropy-based analysis to automatically crop images to a perfect 16:9 aspect ratio, keeping the most interesting part of the image.
- **Image Optimization**: Automatically resizes oversized JPEGs to 4K (3840x2160) to save bandwidth and storage.
- **Auto-Validation**: Every image is verified (decoded) before sync. Corrupt or unsupported files are gracefully skipped, protecting your TV from "bad data" loops.
- **Metadata Auditing**: Automatically dumps TV system info and category lists to JSON for easy reference.
- **Tiny & Fast**: ~8MB Docker image with zero external dependencies.

## 📺 Modern TV Support (Tizen 8.0+)

Unlike generic Samsung TV libraries, this manager includes specific logic for the **2024/2025 Frame TV platform**:
- **Underscore Compatibility**: Handles both `d2d.service.message.event` and the new `d2d_service_message` naming.
- **JSON Unwrapping**: Recursively decodes string-encoded JSON payloads found in newer firmware.
- **Secure Port 8002**: Exclusively uses encrypted WSS connections for stable token persistence.

## 🔄 How it Works (The Sync Cycle)

The manager runs on a configurable interval (default 5 minutes) and performs the following:
1. **Source Loading**: Downloads images from external URLs defined in your sources file.
2. **Optimization**: Resizes oversized local JPEGs to 4K using high-quality Catmull-Rom resampling.
3. **Connectivity Check**: Probes the TV via a silent REST gate. If the TV is in standby or "busy," the cycle skips to prevent annoying popups.
4. **Inventory & Diff**: Compares local files against the TV's inventory using a persistent `mapping.json` file.
5. **Smart Sync**: Uploads new images, deletes removed ones, and reconciles "unknown" images on the TV.
6. **Metadata Audit**: Refreshes a detailed `metadata.json` file containing Serial Numbers, DUIDs, and category lists.
7. **Image Sources**: Automatically pulls high-resolution art from Unsplash collections or NASA's daily astronomy feed.

## 🚀 Quick Start

### Docker (Recommended)

```yaml
services:
  frame-tv-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    restart: unless-stopped
    environment:
      - TV_IPS=192.168.1.106
      - SYNC_INTERVAL_MINUTES=15
      - TZ=America/Denver
    volumes:
      - ./artwork:/data/artwork:ro
      - ./tokens:/data/tokens
```

### First Run
On the first connection, the TV will display an authorization prompt. Select **Allow** using your remote. The manager will securely save the token in your `/data/tokens` directory and will never prompt you again.

## ⚙️ Configuration Reference

### Core Settings
| Variable | Default | Description |
|---|---|---|
| `TV_IPS` | **Required** | Comma-separated list of TV IP addresses. |
| `ARTWORK_DIR` | `/data/artwork` | Path to local image folder. |
| `TOKEN_DIR` | `/data/tokens` | Path for tokens, mappings, and metadata. |
| `SYNC_INTERVAL_MINUTES` | `5` | Minutes between sync cycles. |
| `CLIENT_NAME` | `Frame Art Manager` | The identity displayed on your TV. |

### Slideshow & Display
| Variable | Default | Description |
|---|---|---|
| `MATTE_STYLE` | `none` | Default border (e.g., `shadowbox_polar`). |
| `SLIDESHOW_ENABLED` | `false` | Enable slideshow override. |
| `SLIDESHOW_INTERVAL` | `15` | Minutes between slides. |
| `SLIDESHOW_TYPE` | `shuffle` | `shuffle` or `sequential`. |

### Solar Brightness
| Variable | Default | Description |
|---|---|---|
| `SOLAR_BRIGHTNESS_ENABLED` | `false` | Enable sun-based brightness. |
| `LOCATION_LATITUDE` | *(unset)* | Your latitude (Required for Solar). |
| `LOCATION_LONGITUDE` | *(unset)* | Your longitude (Required for Solar). |
| `BRIGHTNESS_MIN` | `2` | Brightness at night (0-50). |
| `BRIGHTNESS_MAX` | `10` | Brightness at high noon (0-50). |

### Advanced Hardening
| Variable | Default | Description |
|---|---|---|
| `ENABLE_REST_GATE` | `false` | Check Art Mode status via REST before WSS. |
| `REMOVE_UNKNOWN_IMAGES` | `false` | Delete images on TV not found in local folder. |
| `IMAGE_OPTIMIZE_ENABLED` | `false` | Automatically resize JPEGs to 4K. |
| `SMART_CROP_ENABLED` | `true` | Automatically crop to 16:9 using entropy. |
| `TV_MAC` | *(unset)* | MAC address for Wake-on-LAN support. |
| `ARTWORK_SOURCES_FILE` | *(unset)* | Path to `sources.txt` file. |
| `UNSPLASH_ACCESS_KEY` | *(unset)* | Your Unsplash API key. |
| `NASA_API_KEY` | `DEMO_KEY` | Your NASA API key (defaults to demo). |

## 📂 Image Sources (`sources.yaml` or `sources.txt`)

The manager can automatically download and curate images from world-class APIs. To enable this, create a `sources.yaml` (recommended) or `sources.txt` file in your data folder and set `ARTWORK_SOURCES_FILE=/data/sources.txt`.

### 🧪 Source Cookbook (YAML Example)

Simply create a `sources.yaml` file:

```yaml
# sources.yaml
providers:
  nasa:
    - apod
    - search:nebula
  art_institute_of_chicago:
    - search:monet
  unsplash:
    - collection:225444
```

### 🧪 Source Cookbook (Legacy TXT Example)

Simply add these lines to your `sources.txt`:

| Type | Source Command | What it pulls |
|---|---|---|
| **NASA** | `nasa:apod` | Today's Astronomy Picture of the Day. |
| **NASA** | `nasa:search:nebula` | Top 10 high-res nebula photos from NASA. |
| **Art Institute** | `art_institute_of_chicago:search:monet` | 10 masterpieces by Claude Monet. |
| **Art Institute** | `art_institute_of_chicago:search:impressionism` | 10 famous Impressionist paintings. |
| **Unsplash** | `unsplash:collection:225444` | Every photo from a curated Unsplash collection. |
| **Unsplash** | `unsplash:photo:L9W_5q57_V8` | A specific high-res photo by its ID. |
| **Direct** | `https://example.com/art.jpg` | Any direct link to a JPEG or PNG. |

### 🛠 Configuration for APIs

| Variable | Default | Description |
|---|---|---|
| `UNSPLASH_ACCESS_KEY` | *(unset)* | Required for Unsplash. Get one at [unsplash.com/developers](https://unsplash.com/developers). |
| `NASA_API_KEY` | `DEMO_KEY` | Optional for NASA. Defaults to a shared demo key. |

> [!TIP]
> **Pro Tip**: The manager is smart. It only downloads *new* images and automatically tracks Unsplash downloads to comply with their TOS. If you remove a line from `sources.txt`, the image stays in your folder until you manually delete it.

## 🛡️ Robustness & Reliability

This manager is designed to be truly "set-and-forget":
- **Exponential Backoff**: If the TV is unreachable (e.g. Wi-Fi blip), the app waits progressively longer before retrying to prevent network spam.
- **Image Sanity Gate**: If a file in your artwork folder is corrupt or not actually an image, it is logged and skipped automatically.
- **Atomic Writes**: When downloading or optimizing, the app writes to a temporary file first, ensuring you never end up with a half-written, broken image.
- **Silent REST Gate**: The app checks if your TV is in Art Mode *silently* before ever opening a loud "Samsung Remote" connection.

## 📂 File Structure

- `/data/artwork/`: Your `.jpg` and `.png` images.
- `/data/tokens/`:
  - `tv_<IP>.txt`: Encrypted auth token.
  - `tv_<IP>_mapping.json`: Filename to TV content_id map.
  - `tv_<IP>_metadata.json`: TV system audit data (Serial #, Categories).

## 🛠 Building & Development

```bash
# Build the binary
go build -o frame-manager ./cmd/frame-tv-art-manager

# Run with debug logging
export LOG_LEVEL=debug
./frame-manager sync
```

## 📜 License
MIT - Created by MikeO and the Frame TV automation community.