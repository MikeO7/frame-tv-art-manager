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

### 🎨 The "Artifact Edition" Pipeline (Museum-Grade Realism)

The engine has been transformed from a simple image resizer into a professional-grade **Physical Material Simulator**. Every piece of art is processed through a pipeline rooted in academic color science and material reproduction research.

*   **Academic 10px Asymmetric Weave (Zhao 2011 / Peli 1990)**: A procedural warp-and-weft engine that simulates physical canvas fibers. It uses a 10px frequency optimized for 4K tangibility and features **Organic Slub Noise** to break digital patterns.
*   **Physics-Correct Linear Pipeline**: All color mixing and topography calculations are performed in a **64-bit Linear Space**. This ensures that light falloff and pigment blending are mathematically accurate, removing the "digital glow."
*   **Bipolar 3D Topography (Virtual Impasto)**: Instead of flat filters, the engine calculates a **3D Normal Map** for brushstrokes. It applies both **Highlights and Shadows** to the paint ridges, creating physical "body" and volume on your display.
*   **Black-Point Preserving Gamma**: Uses a non-linear contrast model that "pins" the 0.0 (Black) point. This restores rich, inky depth and prevents the "white-washed" or milky film common in digital displays.
*   **Procedural Craquelure (History Simulation)**: Implements a stress-fractal engine that overlays microscopic, age-related cracking in the paint, giving the art a sense of physical age and museum-grade artifacting.
*   **Topography-Aware Archive Varnish**: A liquid varnish simulation that "pools" in the valleys of the canvas weave and thins on the peaks, following real physical fluid dynamics.
*   **Luminance Headroom (Berns 2001)**: Peak brightness is relaxed to **235**, providing punchy whites while maintaining enough "surface headroom" for the TV panel to act as a natural reflective canvas.
*   **Inner Depth Matte Bevel**: Simulates the 1px physical cut of a cardboard matte with light-aware highlights and shadows.

### 🎬 The "Director's Cut" Smart Crop v4.0 (Aesthetic Composition)

When `SMART_CROP_ENABLED` is active, the engine transitions from simple centering to a **Multi-Factor Aesthetic Saliency Map**. This is a 2024-standard composition engine that analyzes every image through a sophisticated multi-pass pipeline:

*   **Boolean Map Saliency (BMS) - Academic Gold Standard**: Implements the state-of-the-art BMS algorithm for non-neural object detection. It identifies whole subjects by detecting topological "surroundedness" across multiple parallel threshold channels.
*   **Parallel Multi-Core Engine**: The saliency analysis is fully multi-threaded, utilizing every CPU core to process threshold maps in parallel. This makes the 4K sync process nearly **5x faster** than traditional sequential models.
*   **Perceptual CIE Lab Color Space**: Unlike basic models that use RGB, this engine performs all color contrast analysis in the **CIE Lab space**. This is perceptually uniform, meaning it "sees" color exactly like the human eye does—picking up on the "soul" of the painting's color palette.
*   **High-Res Micro-Refinement Pass**: A two-pass system that first finds the global "Region of Interest" at a working scale, and then performs a **High-Resolution Fine-Tuning** pass to snap the crop precisely to the sharpest edges of the subject.
*   **Visual Mass Balance & Gaussian Bias**: Incorporates **Visual Weight Analysis** to ensure the composition feels "balanced" on your wall. It perfectly aligns subjects using **Rule-of-Thirds Power Points** and a smooth Gaussian center-bias.
*   **Biometric & Structural Fusion**: Combines **BMS (Objects)**, **Sobel (Structural Edges)**, and **Skin-Tone Heuristics (People)** into a single weighted score for the ultimate focal point.

## Configuration Reference

| Variable | Default | Description |
|---|---|---|
| `TV_IPS` | *(required)* | Your TV's IP address (comma-separate for multiple TVs) |
| `ARTWORK_DIR` | `/data/artwork` | Where your images live |
| `IMAGE_MUSEUM_MODE` | `false` | Enable the "real art" filters |
| `IMAGE_MUSEUM_INTENSITY`| `1` | How heavy the canvas texture should be (1-10) |
| `SMART_CROP_ENABLED` | `false` | Enable the "Director's Cut" aesthetic composition |
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

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) and [AI.md](AI.md) for our engineering standards and AI instructions.

## License

Licensed under the **PolyForm Noncommercial License 1.0.0**. 

Feel free to use this for your home, share it with friends, or tweak the code for your own setup. Just don't sell it or use it for commercial services.