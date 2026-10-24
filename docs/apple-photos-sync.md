# Sending Apple Photos to a Samsung Frame TV

Frame TV Art Manager can receive photos from an iPhone, iPad, or Mac through a
password-protected upload page on your home network. Uploaded photos join the
artwork folder and reach your Samsung Frame TV on the next sync, within 30
minutes by default. No plugins, cloud APIs, or extra accounts are involved.

The same steps are on the project website:
[How to send Apple Photos to a Samsung Frame TV](https://mikeo7.github.io/frame-tv-art-manager/guides/apple-photos-to-samsung-frame-tv.html).

## How it works

```text
iPhone, iPad, or Mac  ──POST /upload──▶  Frame TV Art Manager  ──▶  /data/artwork
   (your home Wi-Fi)                      (Docker or binary)            │
                                                                        ▼
                                                     next sync: prepare and upload
                                                     to every TV in TV_IPS
```

1. Your device sends a JPEG or PNG to `http://SERVER-IP:8080/upload`, signed in
   with HTTP Basic authentication.
2. Frame TV Art Manager checks that the file is a complete JPEG or PNG within
   the size limit and saves it in the artwork folder under a generated name.
   Sending the exact same file again stores it once.
3. On the next sync (every 30 minutes by default) the photo is prepared for the
   screen and uploaded to each TV, with your `MATTE_STYLE` if you set one.

`SERVER-IP` means the machine running Frame TV Art Manager, not the TV.

## Turn on the uploader

The uploader is off by default. Add these settings and restart the container:

```yaml
environment:
  TV_IPS: "192.168.1.150"
  UPLOAD_ENABLED: "true"
  UPLOAD_TOKEN: "a-long-random-password-of-16-plus-characters"
ports:
  - "8080:8080"
```

- The username is always `frame`. The password is `UPLOAD_TOKEN`, which must be
  at least 16 characters. Use a new random value you don't use anywhere else.
- The uploader runs on the health server, port `8080` by default
  (`HEALTH_PORT`). Setting `HEALTH_PORT=0` turns it off.
- Each upload can be up to `MAX_DOWNLOAD_SIZE_MB` (20 MB by default).
- Uploads are unavailable while `DRY_RUN=true`.
- Your device must be on the same network as the server.

## Method 1: the upload page in Safari

Open `http://SERVER-IP:8080/upload` in Safari on your iPhone, iPad, or Mac and
sign in. Tap the drop area and choose one or more photos; the page sends them
one at a time and shows each result. On a Mac you can drag files onto it.

The page only accepts JPEG and PNG. iOS normally converts HEIC photos to JPEG
when a page asks for those formats. If a photo is rejected, use the Shortcut in
Method 2, which converts explicitly.

## Method 2: iPhone or iPad Shortcut

A Shortcut can send your Favorites or an album in one tap, or on a schedule.

### Make the sign-in value

The Shortcut sends an `Authorization` header containing your credentials in
Base64. On a Mac or Linux computer, run this with your own token and copy the
output:

```bash
printf '%s' 'frame:YOUR_UPLOAD_TOKEN' | base64
```

### Build the Shortcut

1. Open the **Shortcuts** app, tap **+**, and name the shortcut
   **Send to Frame TV**.
2. Add **Find Photos**. Filter for favorites or an album, add a filter for
   **Media Type is Image** so videos are skipped, and turn on **Limit** with a
   small number while you test.
3. Add **Repeat with Each** and pass it the photos.
4. Inside the loop, add **Convert Image** and convert **Repeat Item** to
   **JPEG**. iPhone photos are usually HEIC, which the uploader does not accept.
5. Still inside the loop, add **Get Contents of URL**:
   - URL: `http://SERVER-IP:8080/upload`
   - Method: **POST**
   - Request Body: **Form**, with a **File** field named `file` containing the
     converted image
   - Headers: `Authorization` with the value `Basic ` followed by the Base64
     output from above
6. Tap the play button. Each photo should return `File imported successfully`,
   or `File already exists (deduplicated)` if it was sent before.

Apple sometimes renames these actions between iOS versions. If a label doesn't
match, look for the same idea: find photos, convert each to JPEG, and POST it
with the `Authorization` header.

If Shortcuts reports a permission error, check **Settings → Apps → Shortcuts →
Advanced** (or **Settings → Shortcuts → Advanced** on older iOS versions) for
options that allow sharing larger amounts of data.

### Run it on a schedule (optional)

1. Open the **Automation** tab and tap **+**.
2. Choose **Time of Day**, pick a time such as 2:00 AM, and set it to repeat
   daily.
3. Choose to run it immediately rather than asking first, and turn off the
   notification if you don't want one each night.
4. Select **Send to Frame TV**.

Whether iOS runs it without a confirmation depends on your iOS version.

Each run re-sends the photos it finds. Identical files are skipped, but if iOS
produces a slightly different JPEG for the same photo on a later run, that copy
is stored as a new picture. Keep the album or favorites list focused, or use
the Limit option.

## Method 3: Mac script with osxphotos

On a Mac, the free third-party tool [osxphotos](https://github.com/RhetTbull/osxphotos)
can export favorites or an album as JPEGs, and a short script uploads them.

### 1. Allow access to the Photos library

Open **System Settings → Privacy & Security → Full Disk Access** and turn on
Terminal (or iTerm).

### 2. Install osxphotos

```bash
pipx install osxphotos
```

### 3. Create the script

Save this as `~/frame-tv-sync.sh`, fill in your server address and token, and
make it executable with `chmod +x ~/frame-tv-sync.sh`:

```bash
#!/bin/bash
set -euo pipefail

SERVER="http://192.168.1.20:8080"     # the Frame TV Art Manager server, not the TV
UPLOAD_TOKEN="your-upload-token"
EXPORT_DIR="$HOME/Pictures/frame-tv-export"
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"

mkdir -p "$EXPORT_DIR"
osxphotos export "$EXPORT_DIR" \
  --favorite \
  --convert-to-jpeg --jpeg-quality 0.95 \
  --skip-live --skip-raw \
  --update --cleanup

for file in "$EXPORT_DIR"/*.jpeg "$EXPORT_DIR"/*.jpg; do
  [ -f "$file" ] || continue
  curl --fail --silent --show-error \
    -u "frame:$UPLOAD_TOKEN" \
    -F "file=@$file" \
    "$SERVER/upload"
  echo
done
```

The export folder is kept between runs, so `--update` only exports new photos
and files that were already uploaded are recognized as duplicates.

To send an album instead of favorites, replace `--favorite` with
`--album "Frame TV"`.

### 4. Run it every hour (optional)

Save this as `~/Library/LaunchAgents/com.example.frametv-photos.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.example.frametv-photos</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>-c</string>
    <string>~/frame-tv-sync.sh</string>
  </array>
  <key>StartInterval</key>
  <integer>3600</integer>
</dict>
</plist>
```

Load it:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.example.frametv-photos.plist
```

If the script works by hand but fails on schedule, macOS is probably blocking
Photos access for the background job. Granting Full Disk Access to `/bin/bash`
is the usual fix.

## Method 4: curl

Upload one photo from any terminal on your network:

```bash
curl -u "frame:$UPLOAD_TOKEN" \
  -F "file=@/path/to/photo.jpg" \
  http://SERVER-IP:8080/upload
```

Upload a folder, one file at a time:

```bash
for file in ~/Desktop/frame-photos/*.jpg; do
  curl --fail --silent --show-error \
    -u "frame:$UPLOAD_TOKEN" \
    -F "file=@$file" \
    http://SERVER-IP:8080/upload
  echo
done
```

The form field must be named `file`. The endpoint also accepts the image as the
raw request body. It handles one upload at a time, so don't send files in
parallel.

## What happens after upload

1. **Checked.** The file must be a complete, valid JPEG or PNG within
   `MAX_DOWNLOAD_SIZE_MB`.
2. **Deduplicated.** The SHA-256 of the exact file bytes identifies it. If the
   same bytes were uploaded before, the response says
   `File already exists (deduplicated)` and nothing new is stored.
3. **Saved.** The file is written to the artwork folder under a generated name:
   a short label, two hyphens, and the start of its SHA-256 in hex. Your
   original filename is never used. The response JSON includes the saved
   `filename`.
4. **Prepared.** On the next sync, the normal image settings apply: EXIF
   orientation, your `PORTRAIT_MODE` (crop by default, or pad or collage),
   sRGB color conversion, light sharpening, and resizing to exactly 3840×2160
   by default. The prepared version replaces the uploaded file in the folder and
   may get a new name.
5. **Uploaded.** The photo is sent to every TV in `TV_IPS` and appears in Art
   Mode. If a TV is full, it joins the
   [rotation](https://mikeo7.github.io/frame-tv-art-manager/guides/samsung-frame-tv-storage-full-art-rotation.html).

### Removing an uploaded photo

Uploaded photos stay in the artwork folder until you delete them; Frame TV Art
Manager never deletes them from the folder on its own. To take one off the TV,
delete its file from the artwork folder (in Docker, `data/artwork`). The next
sync removes the copy it uploaded from the TV.

## Troubleshooting

| Response | Meaning and fix |
| --- | --- |
| `401` Authentication required | Wrong username or password. The username is always `frame`. Check the Base64 value in your Shortcut. |
| `403` Upload endpoint is disabled | Set `UPLOAD_ENABLED=true` and restart. |
| `403` Upload endpoint is unavailable during dry-run | Turn off `DRY_RUN`. |
| `403` Cross-origin uploads are not allowed | The request came from a web page on a different address. Upload from the server's own `/upload` page, a Shortcut, or curl. |
| `400` file too large | Over `MAX_DOWNLOAD_SIZE_MB` (20 MB by default). Raise the limit or send a smaller export. |
| `400` Invalid or unsafe image | Not a complete JPEG or PNG. HEIC photos end up here; convert to JPEG first. |
| `400` missing 'file' parameter | The form field isn't named `file`. |
| `429` Another artwork import is in progress | Two uploads arrived at once. Send them one after another. |

## Settings

| Variable | Default | Description |
| --- | --- | --- |
| `UPLOAD_ENABLED` | `false` | Set to `true` to turn on `GET` and `POST /upload`. |
| `UPLOAD_TOKEN` | required with uploads | HTTP Basic password for user `frame`; at least 16 characters. |
| `MAX_DOWNLOAD_SIZE_MB` | `20` | Largest accepted upload, in megabytes. |
| `HEALTH_PORT` | `8080` | Port for the health, status, and upload pages; `0` turns them off. |
| `HEALTH_BIND_ADDRESS` | `0.0.0.0` | Address the server listens on; use `127.0.0.1` behind a local reverse proxy. |

## Security

- The uploader is off until you set `UPLOAD_ENABLED=true`, and every request
  needs the password.
- Plain HTTP doesn't encrypt the password or your photos. Keep the uploader on
  your home network, or put a TLS reverse proxy in front of it.
- Never forward port 8080 from your router to the internet.
- Files are fully decoded and size-checked before they're saved, and the server
  chooses the filename.
- Uploaded photos use disk space on the server until you delete them.
