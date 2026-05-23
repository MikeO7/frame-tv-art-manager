# 🎨 Samsung Frame TV Art Manager

[![CI Status](https://github.com/MikeO7/frame-tv-art-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/MikeO7/frame-tv-art-manager/actions)
[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/License-PolyForm_Noncommercial_1.0.0-orange.svg)](LICENSE)

A robust, fully automated sync engine designed to keep your Samsung Frame TV populated with stunning, perfectly formatted art. Whether you want to cycle through local directories or turn your television into a dynamic museum using the **Unsplash Auto-Curation Engine**, this manager handles the entire pipeline quietly in the background.

It handles Tizen WebSocket authentication, image resizing, matting, solar brightness adjustments, and aesthetic cropping so that your television behaves like a premium physical art gallery—never a glowing digital monitor.

---

## 🌟 The Killer Feature: Unsplash Auto-Curation Engine

Stop manually searching for high-res images and transfering them to your TV one by one. The **Unsplash Auto-Curation Engine** transforms your Frame TV into a living art gallery that updates itself automatically.

By linking the manager to Unsplash, you can subscribe to entire curated high-res collections or track specific photographers. Every time a new masterpiece is added to your selected collection, the manager automatically:
1. **Resolves & Streams**: Fetches the high-resolution source from the Unsplash API.
2. **Stable Identity Mapping**: Tracks downloads, prevents re-downloads, and deduplicates identical images using stable content hashes.
3. **Bandwidth Optimization**: Dynamically requests exact `w=3840&q=95&fm=jpg` parameters directly from Unsplash's CDN, ensuring pristine 4K clarity without wasting disk space.
4. **Physical Canvas Enhancement**: Applies our academic canvas weave and 3D paint texture filters to make the photography look like physical prints.
5. **Aesthetic Crop & Upload**: Intelligently crops non-16:9 shots and uploads them silently directly to your TV.

It is completely hands-free. You curating a collection on your laptop or phone is all it takes for your living room wall to update itself a few minutes later.

---

## Key Features

### 📸 Dynamic Unsplash Integration (The Crown Jewel)
*   **Collection Subscriptions**: Enter any public Unsplash collection ID (e.g., `225444` for "Earth from Space") and watch your TV automatically sync new art when the collection changes.
*   **Single-Photo Targets**: Direct-target specific iconic shots by ID (e.g., `L9W_5q57_V8`) for permanent exhibition.
*   **Concurrence & Rate Control**: Features a smart 5-channel API request limiter and caching layer to prevent your developer credentials from being rate-limited.
*   **Official Download Tracking**: Automatically sends download telemetry signals back to Unsplash to credit photographers for their work.

### 🖼️ Museum-Grade Physical Canvas Simulator ("Artifact Edition")
Stop displaying flat, glowing digital JPEGs. The built-in image processor transforms your artwork using advanced rendering pipelines:
*   **Zhao-Peli Asymmetric Canvas Weave**: Simulates physical canvas fibers using a 10px frequency optimized for 4K tangibility, complete with organic "slub" noise to break up repeating digital patterns.
*   **64-Bit Linear Pipeline**: Processes light and colors in a physics-correct linear space to maintain highly accurate gradients and natural pigment blends.
*   **Brushstroke Impasto (3D Topography)**: Generates subtle normal maps from the image luminance, applying directional highlights and shadows to give paint ridges real physical volume.
*   **History-Preserving Craquelure**: Overlays stress-fractal cracks to simulate aged, historic paint structures for classical art pieces.
*   **Black-Point Restoring Contrast**: Pins the absolute black level (0.0) to prevent the "milky wash" common on modern LCD panels, restoring rich depth to shadows.
*   **Luminance Headroom Correction**: Relaxes peak white brightness to 235, preserving surface textures and blending the screen naturally with the surrounding wall lighting.
*   **Depth Matte Bevel**: Adds a subtle, light-aware cardboard matte bevel edge around full-screen images.

### ✂️ "Director's Cut" Smart Crop v4.0 (Aesthetic Composition)
Most resizers blindly center or crop. The Frame TV Art Manager analyzes images using a sophisticated computer vision model:
*   **Boolean Map Saliency (BMS)**: Detects physical subjects and objects based on topological surroundedness and structural contrast instead of simple neural approximations.
*   **Perceptual CIE Lab Space**: Evaluates color contrast inside a uniform color space that matches human vision, preserving the focal point of vibrant color palettes.
*   **High-Res Micro-Refinement**: Performs a quick global ROI sweep and follows it up with a high-resolution edge pass to snap crops perfectly to sharp borders.
*   **Rule-of-Thirds & Visual Mass Balancing**: Integrates Gaussian center bias and visual weight calculations to produce balanced, aesthetic compositions.

### ☀️ Solar Brightness & Auto-Off Syncing
*   **Sun-Aware Backlight Control**: Automatically tracks the solar elevation at your latitude and longitude. It dims the screen to a soft backlight at sunset and brightens it during the peak midday sun.
*   **Smart Auto-Off Hours**: Shuts down the TV's art mode at night (e.g., `22:00`) to save energy. It checks if you are watching a movie or playing a game beforehand, ensuring it never interrupts your active entertainment.

### 🔒 Built for Tizen 8.0+ Stability
*   **Silent REST Gate**: Probes the TV's secure REST endpoints before attempting a WebSocket handshake. If you are watching TV or using an app, the manager backs off silently without causing annoying "Allow Access" prompts to pop up on your screen.
*   **Persistent Handshake Tokens**: Safely caches connection tokens locally, ensuring you only have to click "Allow" on your TV remote once during your initial configuration.

---

## Why This Exists

If you own a Samsung Frame TV running Tizen 8.0+ OS, you have likely realized that almost every open-source sync tool or command-line utility has stopped working. The Tizen 8.0+ operating system firmware introduced strict new WebSocket connection security and updated API endpoints, making it incredibly difficult to upload custom art automatically without using Samsung's slow, manual SmartThings mobile application.

This project was built to solve those Tizen 8.0+ connection quirks while elevating image quality to a museum-grade standard. It doesn't just push images to the screen; it uses academic color science and 3D texture simulation to make your television panel look like a physical canvas.

---

## Quick Start: The Minimalist Setup

If you have a folder of local JPEG images and want to sync them directly to your TV, you can get started in less than two minutes using Docker.

### 1. Structure Your Directory
Create a dedicated project directory on your host computer:
```bash
mkdir -p frame-tv-art/artwork
mkdir -p frame-tv-art/tokens
```

### 2. Create the Docker Compose Configuration
Save the following file as `docker-compose.yml` inside the `frame-tv-art` folder:
```yaml
services:
  frame-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    container_name: frame-tv-art-manager
    restart: unless-stopped
    environment:
      - TV_IPS=192.168.1.150
      - CLIENT_NAME=Living Room Frame TV
      - LOG_LEVEL=info
    volumes:
      - ./artwork:/data/artwork
      - ./tokens:/data/tokens
```
> 💡 *Replace `192.168.1.150` with your Samsung Frame TV's local IP address. You can easily find this in your TV's Network Settings menu.*

### 3. Spin Up the Manager
Run the following command in your terminal:
```bash
docker compose up -d
```

### 4. Authenticate
Drop a few `.jpg` files into the `./artwork` folder. Check your TV screen; a prompt will appear asking to authorize the WebSocket connection from **"Living Room Frame TV"**. Use your TV remote to press **Allow**. The manager will save this token in your `./tokens` folder and handle all future uploads silently!

---

## Setup Guide: Unsplash API & Remote Sources

To unlock the Unsplash Auto-Curation Engine and automatically pull space photos from NASA or fine masterpieces from the Art Institute of Chicago, follow these steps.

### 🔑 Getting Your Unsplash API Credentials
1. Go to the [Unsplash Developer Portal](https://unsplash.com/developers) and log in or create a free account.
2. Click on **New Application**.
3. Agree to the developer terms and give your application a name (e.g., `My Frame TV`).
4. Once created, copy the **Access Key** and **Secret Key** provided on your application dashboard.
5. *(Optional)* Browse Unsplash and find a collection you love. For example, the "Earth from Space" collection URL is:
   `https://unsplash.com/collections/225444/earth-from-space` -> The Collection ID is **`225444`**.

### 1. Update Your Docker Compose Environment
Add your API keys and the path to your sources file to your `docker-compose.yml`:
```yaml
services:
  frame-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    container_name: frame-tv-art-manager
    restart: unless-stopped
    environment:
      - TV_IPS=192.168.1.150
      - CLIENT_NAME=Living Room Frame TV
      - LOG_LEVEL=info
      # Enable API integrations
      - ARTWORK_SOURCES_FILE=/data/sources.yaml
      - UNSPLASH_ACCESS_KEY=your_unsplash_access_key_here
      - UNSPLASH_SECRET_KEY=your_unsplash_secret_key_here
      - NASA_API_KEY=DEMO_KEY
      # Turn on Smart Composition
      - SMART_CROP_ENABLED=true
    volumes:
      - ./artwork:/data/artwork
      - ./tokens:/data/tokens
      - ./config:/data
```

### 2. Write Your `sources.yaml` File
Create a new file named `sources.yaml` inside your local `./config` directory:
```yaml
# ==============================================================================
# Frame TV Art Manager — Automated Remote Art Sources
# ==============================================================================
providers:
  # --- 📸 Unsplash (Stunning Photography) ---
  unsplash:
    - "collection:225444"          # Sync photos from Earth from Space
    - "photo:L9W_5q57_V8"          # Sync a single iconic high-res landscape

  # --- 🚀 NASA (Interstellar Wonders) ---
  nasa:
    - "apod"                       # Automatically download today's NASA Astronomy Photo of the Day
    - "search:james webb"          # Top 10 high-resolution space telescope photos

  # --- 🎨 Art Institute of Chicago (Fine Art Masterpieces) ---
  art_institute_of_chicago:
    - "search:monet"               # Sync 10 historic Claude Monet paintings
    - "photo:16568"                # Sync a specific painting by its ID

  # --- 🔗 Direct Web Links ---
  direct:
    - "https://images.unsplash.com/photo-1607604276583-eef5d076aa5f?q=80&w=3840"
```
Restart your container to apply the changes:
```bash
docker compose down && docker compose up -d
```
The manager will download, optimize, apply canvas textures, smart-crop, and push the collections to your Frame TV in the background.

---

## Configuration Reference

You can customize the manager using the following environment variables:

| Environment Variable | Default | Description |
| :--- | :--- | :--- |
| **`TV_IPS`** | *Required* | Comma-separated list of TV IP addresses (e.g., `192.168.1.150,192.168.1.151`). |
| `CLIENT_NAME` | `Frame Art Manager` | The identity string passed during WebSocket handshake (prevents recurring popups). |
| `SYNC_INTERVAL_MINUTES` | `5` | Time in minutes between checks for new artwork. |
| `ARTWORK_DIR` | `/data/artwork` | Location where local or downloaded artwork is stored inside the container. |
| `TOKEN_DIR` | `/data/tokens` | Location where authentication handshake tokens are cached. |
| `ARTWORK_SOURCES_FILE` | *(empty)* | Path to the YAML or TXT file defining remote image sources. |
| `REMOVE_UNKNOWN_IMAGES` | `false` | When true, automatically deletes any files found on the TV that aren't in your local folder. |
| `DRY_RUN` | `false` | Processes, crops, and textures all images locally without making connections or changes to the TV. |
| `LOG_LEVEL` | `info` | Logging verbosity: `debug`, `info`, `warn`, `error`. |

### 🎨 Fine Art & Matte Settings

| Environment Variable | Default | Description |
| :--- | :--- | :--- |
| `IMAGE_OPTIMIZE_ENABLED` | `true` | Resizes oversized local or downloaded files to clean 4K specs. |
| `IMAGE_MAX_WIDTH` | `3840` | The target horizontal resolution. |
| `IMAGE_MAX_HEIGHT` | `2160` | The target vertical resolution. |
| `IMAGE_JPEG_QUALITY` | `95` | Compression ratio for generated JPEG images. |
| `SMART_CROP_ENABLED` | `false` | Analyzes image saliency to crop 16:9 shots without losing the focal subject. |
| `IMAGE_MUSEUM_MODE` | `false` | Enables physical canvas, light headroom adjustments, and impasto texturing. |
| `IMAGE_MUSEUM_INTENSITY` | `5` | Canvas/impasto depth weighting (integer scale from `1` to `10`). |
| `MATTE_STYLE` | `none` | Surround uploaded images with a virtual matte frame. Options: `none`, `modernthin`, `modern`, `modernwide`, `flexible`, `shadowbox`, `panoramic`, `triptych`, `squares`. |
| `MATTE_COLOR` | *(empty)* | Colors like `polar`, `sand`, `warm`, `neutral`, `sage`, `burgandy`, `navy`, `apricot`. Format via `MATTE_STYLE=shadowbox_polar`. |

### ☀️ Sun & Automation Control

| Environment Variable | Default | Description |
| :--- | :--- | :--- |
| `SOLAR_BRIGHTNESS_ENABLED` | `false` | Automatically dims or brightens the TV based on solar elevation. |
| `LOCATION_LATITUDE` | *(empty)* | Latitude of your home (e.g. `40.7128` for New York). |
| `LOCATION_LONGITUDE` | *(empty)* | Longitude of your home (e.g. `-74.0060`). |
| `LOCATION_TIMEZONE` | *(empty)* | Timezone string matching the [IANA Time Zone database](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (e.g., `America/New_York`). |
| `BRIGHTNESS_MIN` | `2` | Light intensity level in pitch darkness (scale `1-10`). |
| `BRIGHTNESS_MAX` | `10` | Light intensity level at solar zenith. |
| `AUTO_OFF_TIME` | *(empty)* | 24-hour time to automatically power off the TV when in art mode (e.g., `23:30`). |
| `AUTO_OFF_GRACE_HOURS` | `2` | Number of hours to continue sending off commands in case the TV was turned back on manually. |

---

## Development & Custom Builds

If you wish to compile or modify the manager from source, this repository provides a full production suite:

### Prerequisites
*   [Go 1.22+](https://golang.org/)
*   [Make](https://www.gnu.org/software/make/)
*   [pre-commit](https://pre-commit.com/)

### Compilation and Validation Commands
Use the provided `Makefile` to run static checks, formatting, and tests:
```bash
# Build the binary locally
make build

# Run formatting and go modules tidying
make tidy fmt

# Run all code linters (golangci-lint), security scanners (govulncheck), and action tests
make check
```

---

## Contributing

We welcome structural improvements, bug fixes, and feature additions! Please review [CONTRIBUTING.md](CONTRIBUTING.md) and [AI.md](AI.md) for strict code style requirements, linting guidelines, and rules preventing code placeholders.

---

## License

Licensed under the **PolyForm Noncommercial License 1.0.0**. Feel free to use this personal manager in your home, run it on a home server, and customize it. Commercial distribution or integration into commercial smart home systems requires separate licensing.
