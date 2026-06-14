# 🎨 Samsung Frame TV Art Manager

[![CI Status](https://github.com/MikeO7/frame-tv-art-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/MikeO7/frame-tv-art-manager/actions)
[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/License-PolyForm_Noncommercial_1.0.0-orange.svg)](LICENSE)

If you own a Samsung Frame TV, you already know the struggle. Trying to upload your own custom artwork or photos using Samsung's official SmartThings mobile app is incredibly slow, clunky, and frustrating.

To make matters worse, if your TV recently updated to the newer **Tizen 8.0+ OS** firmware, you probably noticed that practically every open-source sync tool or command-line script you found online has completely stopped working. Samsung quietly changed how WebSocket authentication and secure channels behave under the hood.

I built this manager to solve that exact headache. It connects securely to your TV, processes your images so they actually look like real physical canvas prints rather than a giant glowing computer screen, and handles all your scheduling automatically.

---

## 🌟 The Killer Feature: Unsplash Auto-Curation

This is where the tool becomes truly set-and-forget. Stop manually downloading images, resizing them, cropping them to 16:9, and copying them over one by one.

With the **Unsplash Auto-Curation Engine**, you can turn your living room into an ever-changing art gallery:
1. **Find or Create a Collection**: Browse Unsplash and find a collection you love (or create a private one on your phone).
2. **Add the ID to Your Configuration**: Drop the collection's ID into a simple `sources.yaml` file.
3. **Walk Away**: Whenever you add a new photo to that collection on your phone or laptop, the manager automatically catches it, downloads the perfect 4K crop from the Unsplash CDN, applies beautiful physical textures, and pushes it directly onto your wall.

It is completely hands-free. You curating a list of photos while riding the bus is all it takes to refresh your home's decor by the time you walk through the front door.

---

## What It Actually Does

### 🔌 Direct API Integrations (Unsplash, NASA, and More)
*   **Curated Collections**: Sync entire public or private Unsplash folders automatically.
*   **NASA Daily Astronomy APOD**: Automatically pull today's official Astronomy Picture of the Day.
*   **Museum Masterpieces**: Pull classical fine art directly from the Art Institute of Chicago archives (by artist name or specific ID).
*   **Smart Download Tracking**: Automatically credits photographers on Unsplash by sending download telemetry signals, keeping everything above board.

### 🎨 Making Digital Screens Look Like Real Art
Most TVs displaying photos look like... bright, glowing TVs. To break that digital look, this manager lets you optionally run your artwork through a physical material simulator:
*   **Tactile Canvas Weave**: Procedurally overlays canvas fabric fibers so the image looks tangible when you stand close to the screen.
*   **Paint Ridges (3D Impasto)**: Uses image lighting and shadows to generate a 3D normal map, making classical paint brushstrokes look raised and textured.
*   **Linear Color Spacing**: Handles pigment blending in a mathematically correct 64-bit linear space to prevent digital glows and maintain rich color depth.
*   **Toned-Down Backlight Adjustments**: Keeps absolute blacks deep and tones down neon bright whites to help the TV blend into the room's actual ambient light.

### ✂️ Smart Cropping (Not Just Center-Cutting)
If you have a vertical painting or a square photo, typical resizers just stretch it or cut off the sides blindly. This manager runs a visual analyzer to find the actual focal point of the image (whether it is a person, a boat, or a tree) and dynamically crops the 16:9 frame around it so the soul of the composition is never lost.

### ☀️ Auto-Brightness and Smart Power-Off
*   **Sun-Aware Dimming**: Tracks where the sun is in your local sky based on your city's latitude and longitude. It automatically dims the screen as dusk settles and brightens it during sunny afternoons.
*   **Gentle Nightly Shutdowns**: You can set a time for Art Mode to turn off at night (like `22:30`) to save power. The manager checks if you are actively watching a movie or playing a video game first, so it never interrupts your evening.

### 🔒 Rock-Solid Tizen 8.0+ Connection Stability
*   **REST Gate Busy Protection**: Probes the TV's secure REST endpoints before attempting to connect. If your TV is busy running Netflix or YouTube, the manager backs off silently without causing annoying "Allow Access" connection popups to disrupt your screen.
*   **Cached Remote Tokens**: Saves your TV's authorization token locally on the first handshake. You only have to click "Allow" on your TV remote once.

---

## Quick Start: The 2-Minute Setup

If you have a folder of local JPEG images on a home server or computer and want to sync them directly to your TV, here is the absolute quickest way to do it.

### 1. Create Your Local Directories
Make two folders on your host computer:
```bash
mkdir -p frame-tv-art/artwork
mkdir -p frame-tv-art/tokens
```

### 2. Create Your `docker-compose.yml`
Save this file as `docker-compose.yml` inside the `frame-tv-art` folder:
```yaml
services:
  frame-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    container_name: frame-tv-art-manager
    restart: unless-stopped
    environment:
      - TV_IPS=192.168.1.150
      - CLIENT_NAME=Living Room TV
      - LOG_LEVEL=info
    volumes:
      - ./artwork:/data/artwork
      - ./tokens:/data/tokens
```
> 💡 *Be sure to replace `192.168.1.150` with your Frame TV's actual local IP address (you can find this easily in your TV's Network Settings menu).*

### 3. Spin It Up
Run this command in your terminal:
```bash
docker compose up -d
```

### 4. Authenticate
Drop a `.jpg` file into your local `./artwork` folder. Look at your TV; a prompt will pop up asking you to authorize the connection from **"Living Room TV"**. Press **Allow** with your remote. The manager will save that token inside `./tokens` and run silently in the background from now on!

---

## Going Pro: Setting Up Unsplash & Custom Sources

To connect Unsplash, NASA, and other automatic feeds, follow this step-by-step guide.

### 🔑 Getting Your Unsplash API Keys
1. Go to the [Unsplash Developer Portal](https://unsplash.com/developers) and create a free developer account.
2. Click **New Application** and accept their terms (it takes less than a minute).
3. Give your app a name (like `My Home Frame TV`).
4. Copy the **Access Key** and **Secret Key** shown on your dashboard.
5. Browse Unsplash, find a collection you love, and copy its ID from the URL (for example, in `unsplash.com/collections/225444/earth-from-space`, the ID is **`225444`**).

### 1. Update Your `docker-compose.yml`
Update your environment section to include your API keys and link a sources file:
```yaml
services:
  frame-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    container_name: frame-tv-art-manager
    restart: unless-stopped
    environment:
      - TV_IPS=192.168.1.150
      - CLIENT_NAME=Living Room TV
      - LOG_LEVEL=info
      # API Keys & Custom Sources file
      - ARTWORK_SOURCES_FILE=/data/sources.yaml
      - UNSPLASH_ACCESS_KEY=your_real_unsplash_access_key_here
      - UNSPLASH_SECRET_KEY=your_real_unsplash_secret_key_here
      - NASA_API_KEY=DEMO_KEY
      # Turn on Smart Composition
      - SMART_CROP_ENABLED=true
    volumes:
      - ./artwork:/data/artwork
      - ./tokens:/data/tokens
      - ./config:/data
```

### 2. Write Your `sources.yaml`
Create a new file named `sources.yaml` inside your local `./config` folder:
```yaml
# ==============================================================================
# Frame TV Art Manager — Custom Art Feed
# ==============================================================================
providers:
  # --- 📸 Unsplash (Beautiful Photography) ---
  unsplash:
    - "collection:225444"          # Sync an entire collection by its ID
    - "photo:L9W_5q57_V8"          # Target a single specific high-res photo

  # --- 🚀 NASA (Outer Space Photography) ---
  nasa:
    - "apod"                       # Automatically sync today's Astronomy Picture of the Day
    - "search:james webb"          # Pull the top 10 space telescope pictures

  # --- 🎨 Art Institute of Chicago (Masterpieces) ---
  art_institute_of_chicago:
    - "search:monet"               # Sync 10 historic Claude Monet paintings
    - "photo:16568"                # Pull a specific classical piece by its ID

  # --- 🔗 Direct URLs ---
  direct:
    - "https://images.unsplash.com/photo-1607604276583-eef5d076aa5f?q=80&w=3840"
```
Now restart your container to apply the changes:
```bash
docker compose down && docker compose up -d
```
The manager will download your custom feed, run smart crops, generate stable hashes to prevent duplicate downloads, and automatically sync everything with your TV in the background!

## 📸 Syncing Apple Photos & Favorites (iOS & macOS)

You can push your **Favorites**, custom photo albums, or any local photos directly to your Frame TV from an iPhone, iPad, or Mac.

The manager hosts a secure local upload endpoint at `POST http://<server-ip>:8080/upload`. Any JPEG/PNG image sent to this endpoint is automatically optimized to 4K, framed with your chosen matte style, and synced to your TV.

### Prerequisites
1. **Enable Uploads:** Add `UPLOAD_ENABLED=true` to your environment settings.
2. **Expose Ports:** Ensure port `8080` is mapped in your `docker-compose.yml`:
   ```yaml
   ports:
     - "8080:8080"
   ```

---

### Method 1: iPhone & iPad (iOS Shortcut)

You can use the built-in **Shortcuts** app to upload your favorites or a specific album over Wi-Fi with a single tap.

[📥 Download Ready-to-Use iOS Shortcut](https://www.icloud.com/shortcuts/YOUR_SHORTCUT_ID_HERE)
*(After downloading, iOS will prompt you to enter your server's IP address automatically. Alternatively, follow the manual steps below).*

#### 1. Create the Shortcut
1. Open the **Shortcuts** app on your iPhone or iPad.
2. Tap the **`+`** icon in the top right to create a new shortcut. Name it **"Sync to Frame TV"**.
3. Add a **Find Photos** action:
   - Add filter: **`Favorite is Yes`** (or `Album is [Your Album]`).
   - Add filter: **`Media Type is Image`** (excludes videos).
   - Turn on **`Limit`** and set it to **`50`** (recommended to keep syncs fast).
4. Add a **Repeat with Each** action (passing the found photos).
5. Inside the loop, add a **Convert Image** action:
   - Convert **`Repeat Item`** to **`JPEG`** *(Critical: Frame TVs do not support iPhone's default HEIC format)*.
6. Still inside the loop, add a **Get Contents of URL** action:
   - Set the URL to: `http://<YOUR_SERVER_IP>:8080/upload` (replace with your server's IP).
   - Set the method to **`POST`**.
   - Set **Request Body** to **`File`** (or **`Converted Image`**).
7. Done! Tap the **Play** button to test the upload.

> 🔒 **iOS Permissions Troubleshooting:** If Shortcuts reports a permission error when running, open the **Settings** app on your device, select **Shortcuts** → **Advanced**, and turn on the toggles for **Allow Running Scripts** and **Allow Sharing Large Amounts of Data (if visible)**.

#### 2. Automate it (Optional)
You can make this run silently in the background:
1. Tap the **Automation** tab in the Shortcuts app.
2. Tap the **`+`** icon in the top right corner.
3. Tap **Time of Day**:
   - Choose your preferred time (e.g. **`02:00 AM`**).
   - Set repeat to **`Daily`**.
   - Under *How to Run*, select **`Run Immediately`** *(critical to run without manual confirmation prompts)*.
   - Tap **Next**.
4. Set the action to **Run Shortcut** ➔ select **"Sync to Frame TV"**.
5. Turn **Off** the toggle for **`Notify When Run`** to prevent nightly notification spam.
6. Tap **Done**.

---

### Method 2: macOS (Automatic Sync via CLI)

This uses the popular `osxphotos` tool to export your favorites and upload them.

#### 1. Grant Terminal Full Disk Access
Because macOS secures your local Photos library, you must grant your Terminal app access to read it:
1. Open **System Settings** → **Privacy & Security** → **Full Disk Access**.
2. Toggle **Terminal** (or **iTerm**) to **On**.

#### 2. Install osxphotos
Run this command to install the export CLI:
```bash
pip3 install osxphotos
```

#### 3. Save the Sync Script
Create a script named `~/frame-tv-sync.sh`:
```bash
#!/bin/bash
SERVER="http://<YOUR_SERVER_IP>:8080"
TEMP_DIR=$(mktemp -d)

# Add Homebrew and pip bin paths for launchd environment
export PATH="/usr/local/bin:/opt/homebrew/bin:$HOME/Library/Python/3.9/bin:$PATH"

# Export favorites as JPEGs
osxphotos export "$TEMP_DIR" \
  --favorite \
  --convert-to-jpeg \
  --jpeg-quality 0.95 \
  --skip-edited \
  --skip-live \
  --skip-raw \
  --update \
  --cleanup \
  2>/dev/null

# Upload each exported photo
for f in "$TEMP_DIR"/*.jpeg "$TEMP_DIR"/*.jpg; do
  [ -f "$f" ] || continue
  curl -s -X POST -F "file=@$f" "$SERVER/upload"
done

rm -rf "$TEMP_DIR"
```
Make it executable:
```bash
chmod +x ~/frame-tv-sync.sh
```

#### 4. Automate with Launch Agent (Optional)
To sync every hour automatically, save the following as `~/Library/LaunchAgents/com.frametv.photosync.plist`:
```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.frametv.photosync</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>-c</string>
        <string>~/frame-tv-sync.sh</string>
    </array>
    <key>StartInterval</key>
    <integer>3600</integer>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
```
Load the agent:
```bash
launchctl load ~/Library/LaunchAgents/com.frametv.photosync.plist
```

---

## Configuration Reference

You can customize the engine's behavior by adjusting these environment variables:

| Variable | Default | Description |
| :--- | :--- | :--- |
| **`TV_IPS`** | *Required* | Your TV's local IP address (separate with commas for multiple TVs). |
| `CLIENT_NAME` | `Frame Art Manager` | The connection name passed to the TV (prevents recurring popup warnings). |
| `SYNC_INTERVAL_MINUTES` | `5` | How often the script runs in the background to look for new art. |
| `ARTWORK_DIR` | `/data/artwork` | Where downloaded and processed artwork files live. |
| `TOKEN_DIR` | `/data/tokens` | Where TV authentication files are saved. |
| `ARTWORK_SOURCES_FILE` | *(empty)* | Path to your `sources.yaml` or custom source list. |
| `UNSPLASH_APP_ID` | *(empty)* | Your Unsplash API App ID. |
| `UNSPLASH_ACCESS_KEY` | *(empty)* | Your Unsplash API Access Key. |
| `UNSPLASH_SECRET_KEY` | *(empty)* | Your Unsplash API Secret Key. |
| `NASA_API_KEY` | `DEMO_KEY` | Your NASA API Key. |
| `PEXELS_API_KEY` | *(empty)* | Your Pexels API Key. |
| `PIXABAY_API_KEY` | *(empty)* | Your Pixabay API Key. |
| `REMOVE_UNKNOWN_IMAGES` | `false` | Set to true to automatically delete images on your TV that aren't in your local folder. |
| `DRY_RUN` | `false` | Process and texture images locally without actually pushing them to the TV. |
| `LOG_LEVEL` | `info` | Logging verbosity: `debug`, `info`, `warn`, `error`. |

### 🎨 Texture, Matte, & Crop Settings

| Variable | Default | Description |
| :--- | :--- | :--- |
| `IMAGE_OPTIMIZE_ENABLED` | `true` | Automatically downscale massive images to crisp 4K templates. |
| `IMAGE_MAX_WIDTH` | `3840` | The horizontal pixel size target. |
| `IMAGE_MAX_HEIGHT` | `2160` | The vertical pixel size target. |
| `IMAGE_JPEG_QUALITY` | `95` | JPEG encoding quality (1-100). |
| `SMART_CROP_ENABLED` | `false` | Use subject saliency maps to crop images perfectly without cutting off the subject. |
| `IMAGE_MUSEUM_MODE` | `false` | Apply canvas weave, liquid varnish simulations, and 3D impasto brushstroke ridges. |
| `IMAGE_MUSEUM_INTENSITY` | `5` | Canvas weave/paint ridge depth weighting (scale from `1` to `10`). |
| `MATTE_STYLE` | `none` | Add a cardboard frame style around your art: `none`, `modernthin`, `modern`, `modernwide`, `flexible`, `shadowbox`, `panoramic`, `triptych`, `squares`. |
| `MATTE_COLOR` | *(empty)* | Color options: `polar`, `sand`, `warm`, `neutral`, `sage`, `burgandy`, `navy`, `apricot`. Format as `MATTE_STYLE=shadowbox_polar`. |

### ☀️ Ambient Elevation & Shutdown Control

| Variable | Default | Description |
| :--- | :--- | :--- |
| `SOLAR_BRIGHTNESS_ENABLED` | `false` | Track where the sun is to automatically dim or brighten the screen. |
| `LOCATION_LATITUDE` | *(empty)* | Your latitude (e.g. `40.7128` for New York). |
| `LOCATION_LONGITUDE` | *(empty)* | Your longitude (e.g. `-74.0060`). |
| `LOCATION_TIMEZONE` | *(empty)* | Your local timezone matching the [IANA database](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (e.g. `America/New_York`). |
| `BRIGHTNESS_MIN` | `2` | Panel brightness level in pitch darkness (scale `1-10`). |
| `BRIGHTNESS_MAX` | `10` | Panel brightness level at absolute noon. |
| `BRIGHTNESS` | *(empty)* | Manual brightness override (fixed value `0-50`). |
| `AUTO_OFF_TIME` | *(empty)* | Time to automatically shut down Art Mode at night (e.g. `23:30`). |
| `AUTO_OFF_GRACE_HOURS` | `2` | Retry window in hours to keep checking in case the TV was manually powered back on. |

### ⚙️ Advanced System Settings

| Variable | Default | Description |
| :--- | :--- | :--- |
| `TV_MAC` | *(empty)* | MAC address for Wake-on-LAN (optional, wakes sleeping TVs). |
| `ENABLE_REST_GATE` | `false` | Enable the Silent REST Gate to probe port 8001 /ms/art before connecting. |
| `SLIDESHOW_ENABLED` | *(empty)* | Set to `true` to override TV's current slideshow settings. |
| `SLIDESHOW_INTERVAL` | `15` | Minutes between slideshow image changes. |
| `SLIDESHOW_TYPE` | `shuffle` | Slideshow type: `shuffle` or `sequential`. |
| `VERIFY_TLS` | `false` | Enable TLS/SSL certificate verification when connecting to the TV. Frame TVs use self-signed certificates, so this is disabled by default. |
| `SKIP_TLS_VERIFY` | `false` | Skip TLS verification regardless of VERIFY_TLS setting (useful for strictly local setups). |
| `MAX_ARTWORK_IMAGES` | `100` | Maximum number of images to sync. Increase if your collection is larger. |
| `MAX_DOWNLOAD_SIZE_MB` | `20` | Maximum download size per image in megabytes. |
| `HEALTH_PORT` | `8080` | Port for HTTP `/health` and `/status` endpoints (`0` disables the health server). |
| `UPLOAD_ENABLED` | `false` | Set to `true` to enable HTTP `POST /upload` endpoint for photo uploads (syncs to TV). |
| `CONNECTION_TIMEOUT_SECONDS` | `60` | Connection timeout for WSS handshake. |
| `API_TIMEOUT_SECONDS` | `60` | API timeout for art API responses. |
| `UPLOAD_DELAY_MS` | `3000` | Pause between consecutive image uploads in milliseconds. |
| `UPLOAD_ATTEMPTS` | `3` | Number of times to retry a failed upload. |
| `GATE_TIMEOUT_MS` | `10000` | HTTP timeout for the REST gate probe in milliseconds. |
| `PUID` | `0` | Process User ID for Docker permissions. |
| `PGID` | `0` | Process Group ID for Docker permissions. |

---

## Local Compilation & Development

If you want to build or compile the manager from source, this repository has everything set up:

### What you need:
*   [Go 1.22+](https://golang.org/)
*   [Make](https://www.gnu.org/software/make/)
*   [pre-commit](https://pre-commit.com/)

### Build Commands:
```bash
# Build the binary locally
make build

# Run quick format and go mod cleanups
make tidy fmt

# Run all code tests, linters, security audits, and hooks
make check
```

---

## Contributing

Pull requests are always welcome! Check out [CONTRIBUTING.md](CONTRIBUTING.md) and [AI.md](AI.md) for details on code styles and guidelines.

---

## License

Licensed under the **PolyForm Noncommercial License 1.0.0**. Feel free to run this in your home, share it with friends, and tweak it. Commercial distributions or selling integrations requires separate licensing.
