# Frame TV Art Manager

A robust Go service designed for "set-and-forget" artwork synchronization for Samsung Frame TVs. Specifically hardened to handle the stricter security and protocol changes in 2024+ models (LS03D / Tizen 8.0).

## 🌟 Features

- **Automated Synchronization**: Matches a local folder of images to your TV's "My Photos" collection.
- **2024 Model Hardening**: Protocol-aware handling for the latest Samsung firmware (Tizen 8.0 / Y2025 platform).
- **Smart Handshake**: Persistent token management that eliminates "Allow/Deny" popup loops.
- **Solar Brightness**: Automatically adjusts TV brightness based on the sun's elevation at your coordinates.
- **Silent Operation**: Uses a REST-based "Gate" to check TV status before initiating WebSocket connections.
- **Image Optimization**: Automatically resizes oversized JPEGs to 4K (3840x2160) to save bandwidth and storage.
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
| `TV_MAC` | *(unset)* | MAC address for Wake-on-LAN support. |

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