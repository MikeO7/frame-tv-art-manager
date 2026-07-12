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
2. Build a catalog from supported images in the artwork directory.
3. Validate and, when enabled, optimize those images.
4. Connect to each configured TV and read its current Art Mode inventory.
5. Upload missing files and remove tracked files that no longer exist locally.
6. Apply explicitly configured slideshow, brightness, and auto-off settings.

The local artwork directory is the desired collection. Files such as JSON,
YAML, hidden files, temporary files, and symlinks are not treated as artwork.
Remote-source cleanup is conservative: if any source fails to resolve or
download during a cycle, the previous source collection is retained instead of
being pruned from an incomplete result.

Images already present on the TV are tracked in per-TV mapping files under the
token directory. Those mappings are written atomically and keep a backup. The
application does not normally delete TV images it does not own. Enabling
`REMOVE_UNKNOWN_IMAGES` changes that rule and should be treated as a destructive
option.

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
startup; later cycles use `SYNC_INTERVAL_MINUTES`, which defaults to five
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
Content hashes are used to avoid duplicate local files.

### The web uploader

Set `UPLOAD_ENABLED=true`, publish the health port, and open:

```text
http://<server-address>:8080/upload
```

The page accepts JPEG and PNG files. Uploads are size-limited, fully decoded
before being accepted, written atomically, and deduplicated by content hash.
The same endpoint accepts a multipart `POST` with a field named `file`, which is
useful from iOS Shortcuts.

The uploader does **not** implement authentication. Enable it only on a trusted
network, and do not expose port 8080 directly to the public internet. See
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

Optimization is enabled by default. Images larger than the configured target
are resized, while already suitable images take a fast path. The defaults are
3840×2160 and JPEG quality 95.

Portrait images can be handled in three ways:

- `crop` fills the screen by cropping to the target aspect ratio;
- `pad` places the portrait over a padded background;
- `collage` combines portrait images into a landscape composition.

`SMART_CROP_ENABLED` uses the project's content-aware crop path instead of a
simple center crop. `IMAGE_MUSEUM_MODE` applies the optional texture and color
treatment implemented by the optimizer. Both are off by default.

Samsung mattes are selected with `MATTE_STYLE`. Use `none` for full-screen art
or a value such as `shadowbox_polar`. A `mattes.json` file in the artwork
directory can override the matte per filename; it is treated as a control file
and never uploaded as artwork.

## Configuration

`TV_IPS` is the only required setting. It accepts a comma-separated list. The
complete, annotated configuration reference is [`.env.example`](.env.example);
the table below covers the settings most people change.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TV_IPS` | required | Comma-separated TV addresses |
| `ARTWORK_DIR` | `/data/artwork` | Local desired artwork collection |
| `TOKEN_DIR` | `/data/tokens` | Pairing tokens, mappings, and TV state |
| `SYNC_INTERVAL_MINUTES` | `5` | Time between sync cycles |
| `CLIENT_NAME` | `Frame Art Manager` | Name shown in the TV authorization prompt |
| `MATTE_STYLE` | `none` | Global Samsung matte style and color |
| `MAX_ARTWORK_IMAGES` | `0` | Local/source image cap; zero means no configured cap |
| `MAX_DOWNLOAD_SIZE_MB` | `20` | Per-image download and upload body limit |
| `IMAGE_OPTIMIZE_ENABLED` | `true` | Enable validation and resize pipeline |
| `IMAGE_MAX_WIDTH` | `3840` | Target maximum width |
| `IMAGE_MAX_HEIGHT` | `2160` | Target maximum height |
| `IMAGE_JPEG_QUALITY` | `95` | JPEG encoding quality |
| `PORTRAIT_MODE` | `crop` | `crop`, `pad`, or `collage` |
| `SMART_CROP_ENABLED` | `false` | Enable content-aware cropping |
| `IMAGE_MUSEUM_MODE` | `false` | Enable the texture/color treatment |
| `UPLOAD_ENABLED` | `false` | Enable `GET` and `POST /upload` |
| `HEALTH_PORT` | `8080` | HTTP server port; zero disables it |
| `DRY_RUN` | `false` | Plan/log TV reconciliation without TV mutations |
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
- `AUTO_OFF_TIME` uses a 24-hour local time and only acts during its configured
  grace window.
- `TV_MAC` enables Wake-on-LAN before connection attempts.
- `ENABLE_REST_GATE` probes the TV's REST endpoint before synchronization. This
  is firmware-dependent and disabled by default.
- TLS verification is off by default because local Frame certificates are
  commonly self-signed. Set `VERIFY_TLS=true` only when the TV endpoint can be
  verified; `SKIP_TLS_VERIFY=true` takes precedence.

`DRY_RUN` prevents upload, deletion, selection, brightness, slideshow, and
power mutations on the TV. Local source resolution and image preparation still
run, so it should not be treated as a promise that the local data directory is
unchanged.

## Monitoring

The HTTP server exposes:

- `GET /health` — returns 200 before the first cycle and after a successful
  cycle; returns 503 when the most recently completed cycle failed.
- `GET /status` — returns process timing, current stage, last error, cycle
  count, and the latest per-TV status.
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
must remain at or above 100 percent. More detail is in
[`CONTRIBUTING.md`](CONTRIBUTING.md).

## License

The project is licensed under the
[PolyForm Noncommercial License 1.0.0](LICENSE). Personal and other permitted
noncommercial uses are allowed under that license; commercial use is not.
