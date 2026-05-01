# Samsung Frame TV Art Manager

A simple, robust tool to keep a folder of local images or high-res art collections (Unsplash, NASA, Art Institute of Chicago) in sync with your Samsung Frame TV.

It handles the annoying stuff like resizing, cropping, and authentication, so your TV always has a fresh, perfectly formatted rotation of artwork without you ever touching the remote.

---

## Why this exists

If you have a 2024 Samsung Frame TV (LS03D / Tizen 8.0), you probably found that most existing sync tools stopped working. Samsung changed the protocol, and the new firmware is a lot pickier about how it handles WebSocket connections.

I built this manager specifically to handle those Tizen 8.0+ quirks while keeping the image quality at a "gallery" level. It doesn't just upload files; it processes them to look like physical art on your wall.

## Key Features

*   **Set-and-Forget Sync**: Point it at a folder or a `sources.yaml` list. It checks for new art every few minutes and keeps the TV's memory updated.
*   **Built for 4K**: Everything is automatically resized to exactly 3840x2160. No black bars, no blurry scaling.
*   **Automatic Cropping**: Non-16:9 images are intelligently cropped to fill the full screen.
*   **Smart Art Mode Detection**: Probes the TV before connecting so it doesn't interrupt you while you're actually watching a movie.
*   **Museum-Grade Processing**: Optionally applies canvas textures, subtle vignettes, and "Gallery Master" filters to kill the digital glow.
*   **Solar Brightness**: Adjusts the TV's light level based on where the sun is in your specific city.

---

## Quick Start (Docker)

The fastest way to get running is with `docker-compose.yml`:

```yaml
services:
  frame-tv-art-manager:
    image: ghcr.io/mikeo7/frame-tv-art-manager:latest
    restart: unless-stopped
    environment:
      TV_IPS: "192.168.1.100" # Your TV's local IP
      # Optional: add your location for solar brightness
      LOCATION_LATITUDE: "39.7392"
      LOCATION_LONGITUDE: "-104.9903"
      LOCATION_TIMEZONE: "America/Denver"
    volumes:
      - ./data:/data
```

1. Run `docker compose up -d`.
2. Drop some `.jpg` files into `./data/artwork/`.
3. The first time it connects, your TV will ask to "Allow" the connection. Grab your remote and hit **Allow**.

---

## The "Gallery Edition" Pipeline

Most digital images look "flat" on a TV because they're too bright and too sharp. If you enable `IMAGE_MUSEUM_MODE=true`, the manager runs each image through a realism pipeline before uploading:

*   **The Matte Bevel**: Adds a subtle 1px "cut" line to simulate the depth of a physical matte board.
*   **Glow Suppression**: Caps peak brightness so the art reflects your room's light instead of emitting its own "electronic" light.
*   **Archive Tint**: Applies a very light varnish-like warmth to unify a collection.
*   **Substrate Grain**: Adds micro-noise that mimics the physical fibers of paper or canvas.
*   **Inner Depth Shadow**: A soft shadow at the edges to simulate the gap between the frame and the canvas.

## Configuration Reference

| Variable | Default | Description |
|---|---|---|
| `TV_IPS` | *(required)* | Your TV's IP address (comma-separate for multiple TVs) |
| `ARTWORK_DIR` | `/data/artwork` | Where your images live |
| `IMAGE_MUSEUM_MODE` | `false` | Enable the "real art" filters |
| `IMAGE_MUSEUM_INTENSITY`| `1` | How heavy the canvas texture should be (1-10) |
| `SLIDESHOW_ENABLED` | `false` | Let the manager handle the rotation |
| `AUTO_OFF_TIME` | *(unset)* | Time to turn the TV off (e.g., `22:00`) |
| `REMOVE_UNKNOWN_IMAGES` | `false` | Wipe anything on the TV not in your local folder |

### Supported Art Sources

You can mix and match local files with professional sources in a `sources.yaml` file:

*   **Unsplash**: Pull specific collections or search keywords.
*   **NASA**: Today's Astronomy Picture of the Day (APOD) or space searches.
*   **Art Institute of Chicago**: Search for specific artists like "Monet" or "Van Gogh".
*   **Pexels / Pixabay**: High-res photography searches.
*   **Direct URLs**: Just paste a link to a high-res `.jpg`.

---

## Professional Standards

This isn't a script I threw together; it's maintained with a production workflow:
*   **CI/CD**: Every change is tested for build stability and security vulnerabilities.
*   **Security**: Secret scanning and Dependabot are active.
*   **Branch Protection**: The `main` branch is locked to ensure only verified, passing code is shipped.

## License

Licensed under the **PolyForm Noncommercial License 1.0.0**. 

Feel free to use this for your home, share it with friends, or tweak the code for your own setup. Just don't sell it or use it for commercial services.