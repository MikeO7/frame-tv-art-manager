# Frame TV Art Manager

[![CI](https://github.com/MikeO7/frame-tv-art-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/MikeO7/frame-tv-art-manager/actions/workflows/ci.yml)

Frame TV Art Manager is a small, self-hosted service that keeps a folder of
JPEG and PNG images in sync with one or more Samsung Frame TVs. It was built
for the ordinary home-server case: put pictures in a directory, let a container
run in the background, and stop fighting the SmartThings upload flow every time
you want to change the art.

The manager can also download images from a source file, resize them for a 4K
Frame, handle portrait photographs, apply Samsung mattes, and adjust a few Art
Mode settings. It talks directly to the TV over the local network. There is no
Samsung cloud account involved.

The current target is Samsung Frame hardware running Tizen 8.0 or newer. Other
firmware may work, but Samsung's private Art Mode protocol changes between
models and releases.

## What it actually does

Each sync cycle follows the same basic path:

1. Read the optional sources file and download anything missing.
2. Recover and verify the versioned collection manifest against the artwork bytes.
3. Validate and, when enabled, optimize those images.
4. Connect to each configured TV and read its current Art Mode inventory.
5. Upload missing files and remove tracked files that no longer exist locally.
6. Apply explicitly configured slideshow, brightness, and auto-off settings.

The local artwork directory is the desired collection. Files such as JSON,
YAML, hidden files, temporary files, and symlinks are not treated as artwork.
Remote-source cleanup is conservative: if any source fails to resolve or
download during a cycle, the previous source collection is retained instead of
being pruned from an incomplete result.

Removing a remote source does not delete its already-downloaded local file.
Source ownership is never inferred from a numeric filename prefix; delete the
local file explicitly when it is no longer wanted.

Artwork filenames are bounded, readable labels followed by a short SHA-256
prefix of the bytes they currently name, for example
`summer-vacation--8f14e45fceea167a.jpg`. Filename text does not control source
ownership, transform freshness, collage eligibility, or dimensions. Those facts
live in the private versioned collection manifest; version 1 manifests are
migrated automatically. Provider identities are based on provider-owned IDs or
the complete canonical source URL, so source ordering and similar-looking URLs
cannot alias one another.

Owned TV artwork is tracked in checksummed, transactionally replaced per-TV
reconciliation state under the token directory. On upgrade, a valid legacy
filename mapping is adopted once before any TV mutation; corrupt or ambiguous
migration state fails closed. The application does not normally delete TV
images it does not own. Enabling
`REMOVE_UNKNOWN_IMAGES` changes that rule and should be treated as a destructive
option.

### Safety architecture

The process is supervised as one bounded lifetime: HTTP bind failures stop
startup, child failures reach the process result, and shutdown has a configured
deadline. Artwork imports and cycle inventories pass through one transactional
Collection Store with checksummed recovery state and immutable, fully verified
snapshots. A cycle with an incomplete or corrupt inventory stops before TV
planning. Per-TV observations require a verified Frame TV identity, explicit
Art Mode and power facts, and an explicit inventory array; unknown state never
authorizes deletion or display changes. Sensitive state is stored with `0600`
file and `0700` directory modes.

## Quick start with Docker Compose

Create a directory for the service and add this `compose.yaml`:

```yaml
services:
  frame-tv-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    container_name: frame-tv-art-manager
    restart: unless-stopped
    environment:
      TV_IPS: "192.168.1.150"
      CLIENT_NAME: "Frame Art Manager"
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
```

Replace the example address with the TV's stable LAN address, then start it:

```bash
docker compose up -d
docker compose logs -f frame-tv-art-manager
```

On the first connection, the TV should show an Allow/Deny prompt for the value
of `CLIENT_NAME`. Choose **Allow** with the remote. The returned token is stored
under `./data/tokens`, so keep that directory between container upgrades.

Put `.jpg`, `.jpeg`, or `.png` files in `./data/artwork`. The first sync runs at
startup; later cycles use `SYNC_INTERVAL_MINUTES`, which defaults to 30
minutes.

If the container cannot write to the bind mount, pre-create the directories and
give them to the account used by the container:

```bash
mkdir -p data/artwork data/tokens
```

The image starts as root so it can create and optionally `chown` bind-mounted
directories using `PUID` and `PGID`. To run rootless, pre-own both directories,
set Compose `user: "<uid>:<gid>"`, and leave `PUID` and `PGID` unset.

## Supplying artwork

### A local folder

The simplest setup is the one above. Copy supported images into
`data/artwork`; remove them when you no longer want the manager to track them.
Exact encoded-byte SHA-256 hashes are used to avoid duplicate local files.

### The web uploader

Set `UPLOAD_ENABLED=true`, publish the health port, and open:

```text
http://<server-address>:8080/upload
```

The page accepts JPEG and PNG files. Uploads are size-limited, fully decoded
before being accepted, committed through the transactional Collection Store,
and deduplicated by exact encoded-byte hash.
The same endpoint accepts a multipart `POST` with a field named `file`, which is
useful from iOS Shortcuts.

Uploads require HTTP Basic authentication. Use username `frame` and the value
of `UPLOAD_TOKEN` as the password; the token must contain at least 16
characters. Keep the endpoint on a trusted network and do not expose port 8080
directly to the public internet. See
[the Apple Photos guide](docs/apple-photos-sync.md) for an example Shortcut and
macOS workflows.

### Remote sources

Set `ARTWORK_SOURCES_FILE=/data/sources.yaml` and mount that file inside the
container. A structured file looks like this:

```yaml
providers:
  unsplash:
    - collection:225444
    - photo:L9W_5q57_V8

  nasa:
    - apod
    - search:james webb

  art_institute_of_chicago:
    - search:monet
    - photo:20684

  pexels:
    - curated
    - search:architecture

  pixabay:
    - editors_choice
    - search:mountains

  direct:
    - https://example.com/image.jpg
```

Supported providers and credentials are:

| Provider | Commands used by the application | Credential |
| --- | --- | --- |
| Unsplash | `collection`, `photo` | `UNSPLASH_ACCESS_KEY` |
| NASA | `apod`, `search` | `NASA_API_KEY` (`DEMO_KEY` by default) |
| Art Institute of Chicago | `search`, `photo` | none |
| Pexels | `search`, `curated`, `collection`, `photo` | `PEXELS_API_KEY` |
| Pixabay | `search`, `editors_choice`, `photo`, `user` | `PIXABAY_API_KEY` |
| Direct URL | an HTTP or HTTPS image URL | none |

The loader also accepts YAML with a top-level `sources:` list, a plain YAML
list, or a text file containing one source expression per line. Blank lines and
lines beginning with `#` are ignored in text files.

If a configured YAML source file does not exist, the application writes a
commented starter file during startup. API providers are subject to their own
rate limits and terms.

## Image processing

Optimization is enabled by default. Supported images that need a transform are
oriented, cropped or padded, and resized to the exact configured target. The
defaults are 3840×2160 and JPEG quality 95. JPEG and PNG inputs are fully
decoded before use; PNG optimization is enabled by default and preserves PNG
output, including alpha.

Portrait images can be handled in three ways:

- `crop` fills the screen by cropping to the target aspect ratio;
- `pad` places the portrait over a padded background;
- `collage` combines portrait images into a landscape composition.

`PORTRAIT_MODE` is authoritative for every source, including web uploads.
`SMART_CROP_ENABLED` uses a normalized Boolean-map, edge, skin-tone, and color
saliency model instead of a center crop. It falls back to the center crop unless
the saliency score improves by `SMART_CROP_MIN_GAIN`. Protected-region scoring
is enabled with smart crop and penalizes boundaries that bisect dense line/text,
skin-like, or high-contrast detail. `SMART_CROP_PROVIDER=http` can optionally
send a maximum-512-pixel JPEG preview to an operator-controlled HTTP service;
low-confidence, invalid, failed, and timed-out proposals fall back to the local
cropper. The response is JSON with source-pixel `x`, `y`, `width`, `height`, and
`confidence` fields. Linear-light resizing is
enabled by default to avoid dark color fringes, and a conservative luminance
unsharp mask is applied only after resizing. Its configured amount is the 1:1
baseline; the pipeline modestly increases it for downscales and reduces it for
upscales to avoid halos. Random post-8-bit dither is not used because it adds
noise without performing a real precision reduction.

The default `IMAGE_COLOR_PROFILE_POLICY=convert-srgb` converts bounded RGB
matrix/TRC ICC v2/v4 profiles into sRGB before geometry or effects. Unsupported
profiles and standalone PNG chromaticity/gamma metadata fall back to sRGB with
a warning; `assume-srgb` skips conversion and `reject-embedded` fails closed.
Metadata-declared PQ/HLG Rec. 2020 PNG input is tone-mapped to SDR using the
ITU-R BT.2446 Method A luma curve by default. Exact-byte SHA-256 remains the
only deduplication authority; optional difference-hash matching merely reports
probable visual duplicates and never removes either image.
`IMAGE_MUSEUM_MODE` enables the optional creative texture and color
treatment; it is off by default. When enabled, its default intensity of 5 is
the balanced preset intended for most artwork.

Image controls follow a safe-preset model: enabling an optional feature uses a
conservative general-purpose setting, while the adjacent numeric controls allow
fine tuning. Smart crop starts with a 3% improvement threshold, museum mode at
intensity 5/10, and sharpening at amount 0.25 with threshold 4. The defaults
favor preserving source intent; aggressive or destructive behavior remains
explicitly opt-in.

Samsung mattes are selected with `MATTE_STYLE`. Use `none` for full-screen art
or a value such as `shadowbox_polar`. A `mattes.json` file in the artwork
directory can override the matte by current filename or stable artwork key. For
operator files, the stable key is the filename first observed by the manager,
so an override survives an engine-owned optimization rename. `mattes.json` is a
control file and is never uploaded as artwork. The file must contain one JSON object whose
keys are artwork basenames (with optional `_default`) and whose values are
normalized matte names. Invalid or unsafe matte control files stop the Sync
Cycle before any TV work instead of being silently ignored.

## Configuration

`TV_IPS` is the only required setting. It accepts a comma-separated list. The
complete, annotated configuration reference is [`.env.example`](.env.example);
the table below covers the settings most people change.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TV_IPS` | required | Comma-separated TV addresses |
| `ARTWORK_DIR` | `/data/artwork` | Local desired artwork collection |
| `TOKEN_DIR` | `/data/tokens` | Pairing tokens and checksummed reconciliation state |
| `SYNC_INTERVAL_MINUTES` | `30` | Time between sync cycles |
| `CLIENT_NAME` | `Frame Art Manager` | Name shown in the TV authorization prompt |
| `MATTE_STYLE` | `none` | Global Samsung matte style and color |
| `MAX_ARTWORK_IMAGES` | `0` | Local/source image cap; zero means no configured cap |
| `MAX_DOWNLOAD_SIZE_MB` | `20` | Per-image download and upload body limit |
| `IMAGE_OPTIMIZE_ENABLED` | `true` | Enable validation and resize pipeline |
| `IMAGE_MAX_WIDTH` | `3840` | Exact transformed output width |
| `IMAGE_MAX_HEIGHT` | `2160` | Exact transformed output height |
| `IMAGE_MAX_OUTPUT_PIXELS` | `12000000` | Safety cap for configured output pixels |
| `IMAGE_MAX_WORKING_MEMORY_MB` | `512` | Estimated per-transform working-memory cap |
| `IMAGE_JPEG_QUALITY` | `95` | JPEG encoding quality |
| `IMAGE_OPTIMIZE_PNG` | `true` | Apply orientation, crop/resize, and effects to PNG inputs |
| `IMAGE_LINEAR_LIGHT_RESIZE` | `true` | Resize RGB samples in linear light |
| `IMAGE_SHARPEN_AMOUNT` | `0.25` | Scale-aware luminance unsharp-mask baseline (`0` disables) |
| `IMAGE_SHARPEN_THRESHOLD` | `4` | Minimum luminance difference before sharpening |
| `IMAGE_COLOR_PROFILE_POLICY` | `convert-srgb` | Convert supported ICC profiles; `assume-srgb` and `reject-embedded` are available |
| `IMAGE_HDR_TONE_MAP` | `true` | Tone-map metadata-declared PQ/HLG PNG input to SDR |
| `IMAGE_HDR_SOURCE_PEAK_NITS` | `1000` | Assumed HDR mastering peak |
| `IMAGE_HDR_TARGET_PEAK_NITS` | `100` | SDR output peak used by the tone curve |
| `PORTRAIT_MODE` | `crop` | `crop`, `pad`, or `collage` |
| `SMART_CROP_ENABLED` | `false` | Enable heuristic saliency cropping |
| `SMART_CROP_MIN_GAIN` | `0.03` | Minimum improvement over center crop |
| `SMART_CROP_PROTECTION` | `true` | Protect dense detail from crop boundaries when smart crop is enabled |
| `SMART_CROP_PROTECTION_STRENGTH` | `0.35` | Protected-region boundary penalty |
| `SMART_CROP_PROVIDER` | `local` | `local` or opt-in `http` advanced crop service |
| `SMART_CROP_PROVIDER_MIN_CONFIDENCE` | `0.7` | External proposal confidence required before use |
| `IMAGE_PERCEPTUAL_DUPLICATES` | `true` | Report probable visual duplicates without deleting them |
| `IMAGE_PERCEPTUAL_DUPLICATE_DISTANCE` | `6` | Maximum 64-bit difference-hash distance for an advisory |
| `IMAGE_MUSEUM_MODE` | `false` | Enable the texture/color treatment |
| `IMAGE_MUSEUM_INTENSITY` | `5` | Balanced creative-treatment strength (`1`-`10`) |
| `UPLOAD_ENABLED` | `false` | Enable `GET` and `POST /upload` |
| `UPLOAD_TOKEN` | required with uploads | HTTP Basic-auth password (minimum 16 characters) |
| `HEALTH_PORT` | `8080` | HTTP server port; zero disables it |
| `HEALTH_BIND_ADDRESS` | `0.0.0.0` | Local IP address for the HTTP listener |
| `DRY_RUN` | `false` | Read-only collection and TV planning with no durable mutations |
| `REMOVE_UNKNOWN_IMAGES` | `false` | Delete TV art not known to this manager |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

Malformed numeric and boolean values currently fall back to their defaults.
Cross-field and enumerated values such as log level, slideshow type, portrait
mode, and solar coordinates are validated at startup.

### TV behavior

- Set `BRIGHTNESS` for a fixed value, or enable solar brightness with
  `SOLAR_BRIGHTNESS_ENABLED`, latitude, longitude, timezone, and min/max values.
- Setting any `SLIDESHOW_*` variable opts into slideshow override. If none is
  set, the manager preserves the TV's current slideshow configuration.
  Supported intervals are 3, 15, 60, 720, and 1440 minutes; unsupported
  explicit intervals are rejected during startup.
- `AUTO_OFF_TIME` uses a 24-hour local time and only acts during its configured
  grace window.
- The manager preserves the TV's current artwork selection. Selection is not
  changed until the private protocol exposes an observable postcondition that
  can be recovered safely after an interrupted command.
- `TV_MAC` enables Wake-on-LAN only after the TV is positively observed as off.
  Unknown power never authorizes a wake. With multiple TVs,
  the single legacy MAC is ambiguous, so Wake-on-LAN is disabled with a startup
  warning.
- Some Frame firmware reports `standby` while Art Mode is active. The manager
  treats that state as operational only after the Art API positively reports
  Art Mode on; `standby` with Art Mode off remains a known-off state.
- `SHUTDOWN_TIMEOUT_SECONDS` sets the bounded graceful-shutdown window and
  defaults to 30 seconds.
- Malformed numeric and Boolean environment values retain their documented
  fallback for compatibility and emit a structured startup warning. A future
  major release will reject explicitly malformed values.
- `ENABLE_REST_GATE` probes the TV's REST endpoint before synchronization. This
  is firmware-dependent and disabled by default.
- TLS verification is off by default because local Frame certificates are
  commonly self-signed. Set `VERIFY_TLS=true` only when the TV endpoint can be
  verified; `SKIP_TLS_VERIFY=true` takes precedence.

`DRY_RUN` performs read-only collection and TV observation and planning. It
does not create or modify local files, pair a TV, persist tokens or state, send
Wake-on-LAN, expose the upload mutation, or change the TV.

## Monitoring

The HTTP server exposes:

- `GET /live` — reports process liveness.
- `GET /ready` and `GET /health` — return 200 only after supervised startup and
  at least one successful cycle; return 503 while starting, stopping, failed,
  or after an unsuccessful cycle.
- `GET /status` — returns process timing, current stage, last error, cycle
  count, and the latest per-TV status. When the TV reports its internal flash
  capacity and every item in the observed TV Inventory has known byte-size
  evidence, each TV also includes `free_space_bytes` and
  `free_space_percent`; the cycle summary logs the same estimate in readable
  units. Samsung exposes total flash capacity but not used/free bytes, so the
  fields are omitted rather than guessed when any TV artwork is unaccounted.
- `GET /upload` and `POST /upload` — available only when uploads are enabled.

The container healthcheck calls the application's `-healthcheck` command,
which reads `/health` on `HEALTH_PORT`. Set `HEALTH_PORT=0` only if you also
override or disable the image healthcheck in your runtime configuration.

A failed healthcheck does not restart a container by itself. Use a Docker
restart policy or an orchestrator policy if automatic recovery is required.

## Running without Docker

Go uses the toolchain declared in [`go.mod`](go.mod):

```bash
go build -o frame-tv-art-manager ./cmd/frame-tv-art-manager
TV_IPS=192.168.1.150 \
ARTWORK_DIR="$PWD/data/artwork" \
TOKEN_DIR="$PWD/data/tokens" \
./frame-tv-art-manager
```

The binary also supports `--help`, `--version`, and `-healthcheck`.

## Development

The repository keeps its verification commands in the Makefile:

```bash
make tools       # install the pinned local tools
make test        # tests with shuffled order and a coverage profile
make agent-fix   # formatting, Actions validation, lint, hooks, tests, coverage, vuln scan
make docker      # build the local container image
```

`make agent-fix` is the required gate for changes. Aggregate statement coverage
must remain at or above 90 percent. More detail is in
[`CONTRIBUTING.md`](CONTRIBUTING.md).

## License

The project is licensed under the
[PolyForm Noncommercial License 1.0.0](LICENSE). Personal and other permitted
noncommercial uses are allowed under that license; commercial use is not.
