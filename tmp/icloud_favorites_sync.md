# Syncing Apple Photos to Samsung Frame TV

Push your **Favorites**, albums, or any photos from Apple Photos to your Samsung Frame TV — no plugins, no cloud APIs, no third-party accounts.

The manager runs a lightweight HTTP upload endpoint on the same health server it already uses. You send images to it from any device on your LAN (Mac, iPhone, iPad, or a plain `curl` command), and the manager handles the rest: optimization, dedup, matte framing, and TV sync.

---

## How It Works

```
┌──────────────┐     POST /upload      ┌───────────────────────┐
│  Your Mac    │ ──────────────────────▶│  Frame TV Art Manager │
│  or iPhone   │    (LAN / Wi-Fi)       │  (Docker on Linux)    │
└──────────────┘                        └───────┬───────────────┘
                                                │
                                          ┌─────▼─────┐
                                          │ artwork/   │
                                          │ directory  │
                                          └─────┬─────┘
                                                │
                                        auto-optimize,
                                        dedup, matte,
                                        sync to TV
```

1. You send a JPEG or PNG to `POST http://<server>:8080/upload`.
2. The manager saves it to the `artwork/` directory with a deterministic, hash-based filename.
3. On the next sync cycle (default: every 5 minutes), the image is optimized to 4K, framed with your chosen matte, and uploaded to the TV.
4. If you send the same image again, it's silently deduplicated — no wasted space.

---

## Prerequisites

- The manager's health server must be enabled (it is by default on port `8080`). If you set `HEALTH_PORT=0`, the upload endpoint is disabled.
- The upload feature must be enabled via `UPLOAD_ENABLED=true` in your `.env` (disabled by default for security).
- Your Mac or iPhone must be on the same local network as the server.

---

## Method 1: iPhone/iPad — iOS Shortcut

This method uses the built-in **Shortcuts** app on your iPhone or iPad to send your favorited photos directly to the server over Wi-Fi. No SSH, no WebDAV, no extra apps.

[📥 Download Ready-to-Use iOS Shortcut](https://www.icloud.com/shortcuts/YOUR_SHORTCUT_ID_HERE)
*(After downloading, iOS will prompt you to enter your server's IP address automatically. Alternatively, follow the manual steps below).*

### Step 1: Create the Shortcut

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
   - Set the URL to: `http://192.168.1.150:8080/upload` (replace with your server's IP).
   - Set the method to **`POST`**.
   - Set **Request Body** to **`File`** (or **`Converted Image`**).
7. Done! Tap the **Play** button to test the upload.

> 🔒 **iOS Permissions Troubleshooting:** If Shortcuts reports a permission error when running, open the **Settings** app on your device, select **Shortcuts** → **Advanced**, and turn on the toggles for **Allow Running Scripts** and **Allow Sharing Large Amounts of Data (if visible)**.

### Step 2: Automate it (Optional)

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

## Method 2: macOS — Export Favorites Automatically

This method uses a short shell script on your Mac to export favorites from Apple Photos and upload them to the server. No AppleScript permissions headaches — it uses the `osxphotos` CLI tool which reads the Photos database directly.

### Step 1: Grant Terminal Full Disk Access

Because macOS secures your local Photos library, you must grant your Terminal app access to read it:
1. Open **System Settings** → **Privacy & Security** → **Full Disk Access**.
2. Toggle **Terminal** (or **iTerm**) to **On**.

### Step 2: Install osxphotos

```bash
pip3 install osxphotos
```

### Step 3: Create the sync script

Save this as `~/frame-tv-sync.sh`:

```bash
#!/bin/bash
# Sync Apple Photos favorites to Frame TV Art Manager
SERVER="http://192.168.1.150:8080"
TEMP_DIR=$(mktemp -d)

# Add Homebrew and pip bin paths for launchd environment
export PATH="/usr/local/bin:/opt/homebrew/bin:$HOME/Library/Python/3.9/bin:$PATH"

# Export favorites as JPEG (converts HEIC automatically)
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

### Step 4: Run on a schedule (optional)

To sync every hour automatically, add a Launch Agent. Save this as `~/Library/LaunchAgents/com.frametv.photosync.plist`:

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

Load it:

```bash
launchctl load ~/Library/LaunchAgents/com.frametv.photosync.plist
```

#### Syncing a specific album instead of favorites

Replace the `--favorite` flag with `--album "Album Name"`:

```bash
osxphotos export "$TEMP_DIR" \
  --album "Travel 2024" \
  --convert-to-jpeg \
  --jpeg-quality 0.95 \
  --update --cleanup
```

---

## Method 3: Quick Upload with `curl`

The fastest way to push a single image from any terminal:

```bash
curl -X POST \
  -F "file=@/path/to/photo.jpg" \
  http://192.168.1.150:8080/upload
```

Upload an entire folder of photos:

```bash
for f in ~/Desktop/frame-photos/*.jpg; do
  curl -s -X POST -F "file=@$f" http://192.168.1.150:8080/upload
  echo "  → uploaded $(basename "$f")"
done
```

Replace `192.168.1.150` with your server's IP address.

---

## How the Manager Processes Uploads

Once an image arrives at the `/upload` endpoint:

1. **Validation.** The server checks the file is a valid JPEG or PNG and is within the size limit (`MAX_DOWNLOAD_SIZE_MB`, default 20 MB).
2. **Identity & dedup.** A content hash is computed. If the same image was uploaded before, the duplicate is silently discarded.
3. **Saved to artwork/.** The file is written with a deterministic name: `upload-{hash}.jpg`. This name is stable across re-uploads.
4. **Optimization.** On the next sync cycle, the optimizer scales it to 4K (3840×2160), applies your matte style, and applies museum mode filters if enabled.
5. **TV sync.** The image is uploaded to the TV via the Samsung Art API and appears in Art Mode.

### What about removal?

Uploaded photos are treated as **user-owned files** — the manager never deletes them automatically. If you want to remove a photo from the TV:

- Delete the file from the `artwork/` directory (or the Docker volume).
- On the next sync cycle, the manager detects it's gone and removes it from the TV.

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `UPLOAD_ENABLED` | `false` | Set to `true` to enable the `/upload` endpoint. |
| `UPLOAD_MAX_FILE_SIZE_MB` | `20` | Maximum file size per upload in megabytes. |
| `HEALTH_PORT` | `8080` | Port for the health server (and the upload endpoint). |

---

## Security Notes

- The upload endpoint is **disabled by default**. You must explicitly set `UPLOAD_ENABLED=true`.
- The endpoint accepts uploads from **any client on the network** without authentication. Only enable it on a trusted LAN.
- Files are validated (magic bytes, size limits) before being written to disk.
- Filenames are sanitized — the original filename is never used. The server generates a safe, hash-based name.
