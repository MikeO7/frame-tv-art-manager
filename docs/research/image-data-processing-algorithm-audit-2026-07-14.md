# Image and data-processing algorithm audit

Date: 2026-07-14

## Scope and method

This audit covers the repository's image and adjacent data-processing paths:
smart crop, Boolean Map Saliency (BMS), Sobel edges, skin and color saliency,
integral images, resize, padding, rotation and Exif orientation, color handling,
museum-mode tone/gamut/texture effects, sharpening, dithering, JPEG/PNG
encoding, collage generation, solar brightness, input validation, cataloging,
identity parsing, and deduplication.

The implementation was compared with primary sources: published papers,
standards, standards-body material, and upstream Go documentation/source. No
production code was changed. "Latest" below means current as of the audit date;
newer learned crop systems are noted, but are not automatically recommended for
this dependency-light Go application.

## Post-audit remediation

The audit text and inventory below describe the pre-remediation implementation.
The following corrections were implemented after the review:

- corrected NOAA-style solar time/elevation math with reference vectors;
- repaired BMS surroundedness with the paper's opening/dilation stages,
  standard sRGB/D50 Lab conversion, and all 34 Sharma CIEDE2000 reference pairs;
- normalized smart-crop features, centered ties, added boundary refinement,
  introduced a configurable minimum gain over center crop, and added a
  versioned synthetic crop golden corpus covering portraits/skin tones,
  animal-like texture, text/line art, landscapes, multiple subjects, edge
  distractors, and already-good compositions with center-crop comparisons;
- oriented images before fast-path decisions, added PNG `eXIf` support, and
  strictly require the Exif orientation tag's SHORT/count/value contract;
- replaced filename-authorized caching with durable transform metadata and
  content-addressed derivative names;
- added full output decode validation with expected format/dimensions,
  symlink/race-resistant catalog hashing, output-pixel and working-memory limits;
- added linear-light resizing, bounded scale-aware luminance sharpening only
  after resampling, embedded-color-metadata policy, and individual PNG transformation;
- removed random post-8-bit dithering because it was not tied to a precision
  reduction;
- parameterized collage geometry, made mixed PNG/JPEG output order-independent,
  admitted the combined input working set before full pixel decode, and made
  `PORTRAIT_MODE` authoritative for all source origins; and
- removed the dead luminance option and documented the new image controls.

A follow-up implementation added the remaining suitable extensions identified
after the audit:

- bounded RGB matrix/TRC ICC v2/v4 conversion to sRGB, with explicit fallback
  warnings for unsupported profile types and a strict rejection mode;
- metadata-gated PQ/HLG Rec. 2020 PNG conversion to SDR using the ITU-R
  BT.2446 Method A luma curve with configurable source and target peaks;
- protected-region crop scoring for dense line/text, skin-like, and
  high-contrast detail;
- an opt-in, timeout-bounded HTTP crop-provider protocol with strict geometry
  and confidence validation plus deterministic local fallback; and
- non-destructive 64-bit difference-hash similarity advisories. Exact SHA-256
  remains authoritative and perceptual similarity never authorizes deletion.

The final review additionally made embedded-metadata inspection streaming
(JPEG stops at SOS and PNG stops at IDAT), restricted HDR tone mapping to the
declared Rec. 2020 PQ/HLG contract the converter supports, aligned preflight
with execution for unsupported PNG color hints, and reuses ingress decodes for
perceptual hashes instead of decoding every catalog item twice.

The resulting settings use conservative, general-purpose presets when a feature
is enabled: smart crop requires a 3% saliency gain, museum mode uses intensity
5/10, and luminance sharpening uses amount 0.25 with threshold 4. Each remains
independently tunable for operators who need a stronger or weaker result.

The crop corpus is a deterministic regression guard, not evidence of
an “80% of people” preference rate. Establishing that claim still requires a
held-out real-image corpus with licensed redistribution, blinded human labels,
and comparison against center crop and competing systems. Until that study is
performed, the 3% gate is deliberately described as a conservative engineering
default rather than an empirically calibrated preference threshold.

The creative museum-mode operations remain deliberately opt-in. Exact
encoded-byte SHA-256 remains the mutation-safe identity; perceptual similarity
is reported separately and cannot authorize deletion.

## Complete algorithm and transform inventory

| Algorithm / transform | Implementation | Purpose | Audit status | Finding / recommendation |
| --- | --- | --- | --- | --- |
| Encoded-byte limit | [`collection.readBounded`](../../internal/collection/validate.go#L58) | Bound untrusted import size | Sound | Retain; it complements decoded-pixel limits |
| Format sniffing and extension match | [`decodeConfiguration`](../../internal/collection/validate.go#L79), [`validateDownloadedImage`](../../internal/sources/loader_download.go#L123) | Reject mislabeled/unsupported images | Sound | Retain type and decoded-fact consistency checks |
| Header-first dimension/pixel validation | [`validateDimensions`](../../internal/collection/validate.go#L109), [`decodeInputConfig`](../../internal/optimize/limits.go#L11) | Bound decoder allocation | Sound for input | Retain 40M input cap; add output-memory admission |
| Full-stream decode validation | [`fullyDecode`](../../internal/collection/validate.go#L97) | Detect corrupt/truncated pixels | Sound at ingress | Reuse equivalent validation for produced files |
| Header-only output validation | [`ValidateImage`](../../internal/optimize/resize.go#L188) | Cheap output check | Defective contract | `DecodeConfig` is not a corruption check; full-decode and check expected facts |
| JPEG Exif/TIFF orientation parse | [`ReadOrientation`](../../internal/optimize/exif.go) | Read orientation tag | Mostly sound | Enforce SHORT/count/value; warn on malformed data; support PNG eXIf if PNG remains supported |
| Exif 1-8 pixel transforms | [`RotateImage`](../../internal/optimize/rotate.go) | Canonicalize display orientation | Mostly sound | Mapping is correct for zero-origin decoder images; orient before fast-path dimension decisions |
| Normal center aspect crop | [`cropRectForAspect`](../../internal/optimize/effects.go#L34) | Fill target aspect | Sound baseline | Keep as conservative fallback and benchmark comparator |
| Smart-analysis nearest-neighbor scale | [`findBestDirectorCrop`](../../internal/optimize/saliency.go#L14) | Reduce analysis to 256 px | Poor | Replace with antialiased downscale |
| Boolean Map Saliency | [`generateBMSMap`](../../internal/optimize/bms.go#L11) | Nominal object/surroundedness map | Defective | Flood fill cannot affect true pixels; implement the paper or rename/remove |
| Sobel gradient magnitude | [`calculateSobelEdge`](../../internal/optimize/sobel.go#L8) | Structural saliency/refinement | Math works; scale defective | Normalize feature response before fusion |
| Binary YCbCr skin heuristic | [`calculateSkinProbability`](../../internal/optimize/sobel.go#L96) | Favor skin-like pixels | Brittle | Remove/downweight or use optional face/person semantics with fairness tests |
| RGB-to-CIELAB | [`rgbToLab`](../../internal/optimize/color.go#L129) | Perceptual color saliency | Defective | Correct sRGB transfer, D65/D50 adaptation, white normalization, and vectors |
| CIEDE2000 | [`ciede2000`](../../internal/optimize/color.go#L20) | Perceptual color difference | Formula plausible; inputs invalid | Use correct Lab and full Sharma/CIE vectors |
| Rule-of-thirds/center/edge priors | [`generateSaliencyMap`](../../internal/optimize/saliency.go#L196) | Aesthetic position weighting | Unvalidated heuristic | Calibrate through ablation and crop benchmark |
| Weighted saliency fusion | [`generateSaliencyMap`](../../internal/optimize/saliency.go#L220) | Combine BMS/Sobel/skin/color/aesthetics | Defective calibration | Normalize features, add confidence, preserve centered failure behavior |
| Spectral-residual saliency | Not present in production code | Frequency-domain saliency | Absent | Do not claim it is used; another unbenchmarked heuristic would not repair this cropper |
| Summed-area/integral image | [`calculateIntegralImage`](../../internal/optimize/integral.go) | O(1) window saliency sums | Sound | Inclusive rectangle convention is consistent; retain tests |
| Sliding crop-window scan | [`scanBestWindow`](../../internal/optimize/saliency.go#L61) | Maximize retained saliency | Tie bug | Center equal/low-confidence cases rather than taking top/left |
| Five-point Sobel micro-refinement | [`refineOffset`](../../internal/optimize/saliency.go#L97) | Nominal boundary alignment | Conceptually unsafe | Replace with subject-retention/boundary-crossing score and margin |
| Catmull-Rom crop/resize/upscale | [`centerCrop`](../../internal/optimize/effects.go#L23), [`padPortrait`](../../internal/optimize/padding.go#L13) | Produce target pixels | Good kernel, wrong color domain | Retain kernel initially; use profile-normalized linear/high-precision pixels |
| BiLinear 1/8 scale + separable box blur | [`blurImage`](../../internal/optimize/padding.go#L36) | Padded portrait background | Reasonable creative effect | Parameterize/visually test; it is gamma-space and fixed-scale |
| Portrait foreground composite | [`padPortrait`](../../internal/optimize/padding.go#L13) | Preserve portrait with background bars | Reasonable | Document alpha/color behavior; test extreme aspect ratios |
| Generic image-to-8-bit RGBA conversion | [`toRGBA`](../../internal/optimize/effects.go#L13) | Establish a common pixel buffer | Defective color contract | Drawing copies decoded values but does not transform ICC/gAMA/primaries into sRGB |
| Full-strength 3x3 sharpen | [`sharpenWithWorkers`](../../internal/optimize/effects.go#L139) | Increase apparent detail | Over-aggressive | Use bounded scale-aware unsharp masking or skip when not resampled |
| Random same-channel ±1 jitter | [`ditherWithWorkers`](../../internal/optimize/effects.go#L65) | Nominal banding reduction | Misapplied | Remove from 8-bit-to-JPEG; dither only a defined final precision reduction |
| RMS contrast statistic | [`calculateRMSContrast`](../../internal/optimize/museum.go#L63) | Normalize collection contrast | Uncalibrated domain | Use a declared metric and verify output converges to target |
| Gamma contrast LUT | [`applyContrastAndGamut`](../../internal/optimize/museum.go#L181) | Museum contrast look | Direction suspect; transfer approximate | Correct direction with synthetic tests; use standard sRGB transfer |
| Neutral-mix "gamut compression" | [`processGamutPixel`](../../internal/optimize/museum.go#L143) | Reduce saturation | Not gamut mapping | Rename as desaturation or use target-profile-aware mapping |
| Scharr-derived impasto | [`calculateScharrImpasto`](../../internal/optimize/canvas.go#L86) | Edge-reactive texture | Creative heuristic | Keep only as opt-in; visually test haloing and encoded-space blend |
| Procedural weave/varnish LUT | [`calculateWeave`](../../internal/optimize/lut.go#L102) | Canvas texture | Creative heuristic | Deterministic, but 20 px periodicity can tile visibly at 4K |
| Procedural Voronoi-like craquelure LUT | [`initializeCraquelure`](../../internal/optimize/lut.go#L17) | Crack texture | Creative heuristic | Deterministic, but 256 px periodicity can tile visibly |
| Soft-light canvas composite | [`processCanvasPixel`](../../internal/optimize/canvas.go#L56) | Apply texture/color cast | Creative heuristic | Encoded-RGB effect; make no physical/standards accuracy claim |
| Highlight clamp/gray mix/grain | [`polishPixel`](../../internal/optimize/museum_polish.go#L10) | Final museum look | Destructive creative effect | Document clipping/desaturation or use a smooth tone curve; avoid double noise |
| JPEG encoding | [`encodeOptimizedTemporary`](../../internal/optimize/resize_rewrite.go#L98) | TV-compatible output | Compatible, lossy | Baseline 8-bit 4:2:0; avoid repeat encoding and benchmark chroma detail |
| PNG encoding | [`processCollagePair`](../../internal/optimize/collage_pipeline.go#L147) | Preserve PNG collage extension | Incomplete contract | Decide alpha/profile/eXIf/output-space semantics; individual PNGs bypass optimization |
| Two-panel collage layout | [`CreateCollage`](../../internal/optimize/collage.go#L9) | Pair portraits into landscape | Geometry/config defect | Parameterize canvas; pixels are always 3840x2160 although name uses config |
| Collage center/smart crop | [`CreateCollage`](../../internal/optimize/collage.go#L18) | Fill each half-panel | Inherits crop findings | Use same benchmark/fallback; make mixed-format behavior explicit |
| Exact SHA-256 digest/dedup | [`readAndValidate`](../../internal/collection/validate.go#L27), catalog/source hashing | Exact encoded-byte identity | Sound for scope | Keep authoritative; call it byte identity, not visual/pixel identity |
| Canonical filename/dimension parse | [`artwork.ParseDimensions`](../../internal/artwork/identity.go#L27) | Fast-path identity | Too permissive | Anchor canonical format and include transform fingerprint |
| Worker-count admission | [`resources.Controller.Run`](../../internal/resources/controller.go#L133) | Bound concurrent transforms | Incomplete | Controls count, not bytes; add peak-memory admission and output-pixel cap |
| `NormalizeLuminance` configuration | [`optimize.Config`](../../internal/optimize/resize.go#L26) | Nominal luminance normalization switch | Dead setting | Defaults true but is never read; remove it or implement and document a measured algorithm |
| Solar position | [`sunElevation`](../../internal/brightness/solar.go#L17) | Drive ambient brightness | Defective | Correct Julian date/equation of time; add NOAA/NREL vectors |
| Kasten-Young air mass | [`brightnessFromElevation`](../../internal/brightness/solar.go#L95) | Shape brightness by elevation | Air-mass formula sound; policy mislabeled | Separate cited air mass from application attenuation/mapping |
| TV upload byte transport | [`samsung` upload path](../../internal/samsung/protocol_upload.go) | Send prepared artwork | No image transform | It transports the selected encoded bytes; no hidden resampling, color conversion, or crop was found |

## Executive conclusion

The pipeline has good durability and input-validation foundations, and
Catmull-Rom resizing, the integral-image implementation, the eight JPEG Exif
orientation transforms, and exact SHA-256 identity are defensible choices.
However, four correctness defects should be fixed before tuning artistic
weights:

1. The solar-position calculation is materially wrong and can select night
   brightness during daylight.
2. The BMS flood fill cannot measure surroundedness and reduces to a small set
   of luminance threshold masks.
3. RGB-to-Lab conversion is not CIELAB, so CIEDE2000 is fed invalid values.
4. Optimized filenames encode dimensions only, allowing stale transforms and
   an exact-size Exif-oriented JPEG to be marked optimized without being
   oriented or processed.

The current smart crop is therefore not an accurate implementation of its
stated components and is not validated as a safe aesthetic cropper. The best
next move is not to retune its constants. First establish mathematical golden
tests and a representative crop benchmark; then repair or replace components
behind a conservative center-crop fallback.

## Prioritized findings

| Priority | Finding | User-visible risk | Recommended disposition |
| --- | --- | --- | --- |
| P0 | Solar Julian date and equation-of-time formulas are wrong | Night/minimum brightness can be chosen in daylight | Replace with a tested NOAA implementation or NREL SPA-derived implementation and authoritative golden vectors |
| P0 | BMS border flood fill cannot include threshold-positive pixels | "Object saliency" is only averaged bright-pixel thresholds | Implement the published algorithm faithfully or remove the BMS name/component |
| P0 | RGB-to-Lab omits standard sRGB transfer, white normalization, and chromatic adaptation | Color saliency is biased and CIEDE2000 inputs are invalid | Implement standards-derived conversion and Sharma/CIE test vectors |
| P0 | Transform identity records dimensions, not algorithm/config/version | Config/code changes leave stale art; exact-size Exif images can be mislabeled | Add a transform fingerprint and make orientation part of preflight/rewrite decisions |
| P0 | Preflight and fast path trust filename dimensions instead of verified decoded bounds | Spoofed/mislabeled optimized names can bypass the pixel contract | Compare snapshot and decoded facts with target before skipping; fail conservatively |
| P1 | Go RGBA conversion is described as color normalization but ignores source profiles | Wide-gamut/tagged images can shift color and output loses provenance | Convert profile-aware to a declared output space, or detect/warn/reject unsupported profiles |
| P1 | Smart-crop feature scales and tie behavior are uncalibrated | Edges can dominate; blank/uniform images crop at the top/left | Normalize features, center ties, replace micro-refinement, and benchmark subject retention |
| P1 | Fixed binary skin box receives a 25% weight | Earth tones can be mistaken for people; real people can be missed across lighting/skin tones | Remove/downweight or use optional face/person semantics with fairness tests |
| P1 | Museum "contrast" moves in the apparent wrong direction and "gamut" is not gamut mapping | Unpredictable contrast, clipping, desaturation, and hue changes | Treat as an opt-in creative look; correct direction and remove technical normalization/gamut claims |
| P1 | Full-strength sharpening and post-8-bit random dither are unconditional on rewritten JPEGs | Halos/noise, reduced compressibility, and unnecessary generational damage | Use bounded scale-aware sharpening; remove JPEG dither unless quantizing from higher precision |
| P1 | `ValidateImage` is header-only despite claiming a corruption check | Truncated output can pass publication validation | Full-decode produced files and verify format/dimensions before publication |
| P1 | Output dimensions may be configured as 16384x16384 with no byte-based admission | One RGBA buffer is about 1 GiB and multiple full-frame buffers can exhaust memory | Add an output-pixel/estimated-peak-memory cap and memory-weighted admission |
| P1 | Operator docs overstate and misdescribe processing/authentication | Operators can expect resizing/PNG processing or unauthenticated examples that do not match runtime | Correct README and Apple Photos guide when behavior is changed |
| P2 | Collage pixels are always 3840x2160 but filenames use configured dimensions | Non-default configuration can produce lying filenames | Parameterize collage geometry or enforce the target constant |
| P2 | Individual PNGs are accepted but never optimized; PNG orientation is ignored | Inconsistent results between source types | Define PNG support explicitly and handle PNG eXIf/profile/alpha semantics |
| P2 | Catalog hashing has a directory-entry/open race | A path replaced during rebuild can be hashed under stale assumptions | Open safely and verify the file identity/type after open |
| P2 | Exact SHA-256 dedup is not visual dedup | Re-encodes and metadata variants remain separate | Keep exact dedup as authority; optionally report perceptual near-duplicates without destructive auto-removal |

## Detailed findings

### 1. Solar brightness is mathematically incorrect

[`julianCentury`](../../internal/brightness/solar.go#L44) computes a Julian day
without the Gregorian correction `B = 2 - A + floor(A/4)`. For modern dates
that is a 13-day offset. The equation of time in
[`solarDeclination`](../../internal/brightness/solar.go#L62) is also not NOAA's
formula: it omits required eccentricity factors and the `y^2 sin(4L0)` term,
combines terms with different signs, and invents a large cross-term.

A reproducible spot check at Denver (39.7392, -104.9903) on 2026-07-14
00:00:00 UTC gives approximately:

| Calculation | Equation of time | Geometric elevation |
| --- | ---: | ---: |
| Repository | +167.51 minutes | -6.75 degrees |
| Corrected Gregorian Julian date + NOAA equations | -5.86 minutes | +26.03 degrees |

That is not a precision-only discrepancy: it changes the sign of the solar
elevation and therefore switches the TV from daytime brightness to the minimum.
The existing tests allow a very loose Julian-century tolerance and broad
day/night ranges, so they do not detect the defect.

The Kasten-Young relative air-mass expression at
[`solar.go:108-109`](../../internal/brightness/solar.go#L108) matches the
published formula. The following `0.7^(airmass^0.678)` attenuation curve is a
separate policy, not part of Kasten-Young. It yields 0.7 even near zenith, so
[`brightnessFromElevation`](../../internal/brightness/solar.go#L95) cannot
actually reach the configured maximum despite its comment. Keep it only if it
is documented and calibrated as an application-specific brightness curve.

For a dependency-light implementation, NOAA's published equations are
adequate if transcribed exactly and tested against authoritative examples. If
high precision is desired, NREL's Solar Position Algorithm reports uncertainty
of about 0.0003 degrees across years -2000 to 6000.[^noaa-details] [^nrel-spa]
Kasten and Young should be cited only for air mass.[^kasten-young]

### 2. BMS is not Boolean Map Saliency

The repository says it implements BMS surroundedness
([`bms.go:8-10`](../../internal/optimize/bms.go#L8)), but
[`tryEnqueue`](../../internal/optimize/bms.go#L57) enqueues only pixels for
which `boolMap` is false. Consequently, `bg` can never be true where `boolMap`
is true. The final condition `boolMap[i] && !bg[i]` at
[`bms.go:126-129`](../../internal/optimize/bms.go#L126) therefore returns every
threshold-positive pixel, including border-connected ones. Flood filling has
no effect on the output.

The published BMS method thresholds all Lab channels, processes each Boolean
map and its inverse, removes border-connected regions after opening, dilates
and L2-normalizes attention maps, aggregates them, and blurs the result. The
repository instead uses five fixed thresholds on gamma-coded luminance and
omits those operations
([`bms.go:15-44`](../../internal/optimize/bms.go#L15)). Its result is an average
of five bright-pixel masks, not a surroundedness map.[^bms]

This invalidates the nominal 40% "object" contribution in
[`saliency.go:250-253`](../../internal/optimize/saliency.go#L250). Add topology
tests where identical bright regions touch and do not touch the border; the
current implementation should fail them.

### 3. Smart crop is not calibrated or safety-tested

Several independent issues compound:

- The 256-pixel analysis image is built with nearest-neighbor scaling
  ([`saliency.go:18-27`](../../internal/optimize/saliency.go#L18)). Upstream
  `x/image/draw` calls this a very-low-quality interpolator; it aliases edges
  that are then scored as structural saliency.[^ximage-draw]
- Sobel magnitude is divided by 255, not by the kernel's maximum response, so
  it can exceed 1 by roughly 5.66. The other fused terms are capped or binary.
  The claimed 20% Sobel weight can therefore dominate the 40/25/15 weighting
  ([`sobel.go:42-47`](../../internal/optimize/sobel.go#L42),
  [`saliency.go:250-253`](../../internal/optimize/saliency.go#L250)). Existing
  tests explicitly accept values over 1 instead of validating calibrated maps.
- A uniform or zero saliency map selects position zero because
  [`scanBestWindow`](../../internal/optimize/saliency.go#L61) initializes the
  best offset at zero and replaces it only on strict improvement. Smart crop
  therefore changes the normal centered fallback into a top/left crop.
- The comment promises a plus/minus 5% refinement but the code searches 2%
  ([`saliency.go:44-46`](../../internal/optimize/saliency.go#L44),
  [`saliency.go:102-108`](../../internal/optimize/saliency.go#L102)). More
  importantly, it calls five Sobel samples at the crop corners and center a
  "boundary" score ([`saliency.go:143-157`](../../internal/optimize/saliency.go#L143)).
  Maximizing that score can place a crop boundary on a subject edge; it does
  not measure subject containment or saliency crossing the boundary.
- Averaging Lab coordinates and comparing each pixel to that average is not a
  robust background model, even after Lab conversion is fixed
  ([`saliency.go:171-194`](../../internal/optimize/saliency.go#L171)).
- The fixed YCbCr box is a binary classifier, not a probability
  ([`sobel.go:96-104`](../../internal/optimize/sobel.go#L96)). Giving it 25% of
  the score can favor wood, sand, or paintings while failing people under
  lighting and skin-tone variation. Current research explicitly targets color
  invariance and unconstrained illumination rather than a universal fixed
  box.[^skin-cvprw]

Recent accepted crop research models composition, crop-versus-discard regions,
semantic context, rank consistency, and guidance such as shift/zoom/view
change. Venus and PhotoFramer are current CVPR 2026 examples; spatial-aware
ranking (CVPR 2023) and crop/discard attractiveness (WACV 2022) are more direct
algorithmic references.[^venus] [^photoframer] [^spatial-rank] [^crop-discard]
These learned systems are evidence that the heuristic is not state of the art,
not a recommendation to add a large model to the core engine. A defensible
dependency-light design is:

1. antialiased analysis scaling;
2. individually normalized and ablated feature maps;
3. center-preserving tie/failure behavior;
4. explicit penalty for saliency or semantic edges cut by the crop, plus a
   subject-safe margin;
5. comparison against center crop on a versioned, representative labeled
   corpus using retention, composition, and human preference metrics.

The existing synthetic checkerboard test is useful as a unit test but is not a
crop-quality benchmark. The MIT Saliency Benchmark demonstrates the expected
discipline of held-out human fixation data and multiple metrics, while also
showing why center bias must be treated carefully.[^mit-saliency] [^center-bias]

### 4. Color conversion and color management are not standards-correct

[`rgbToLab`](../../internal/optimize/color.go#L129) uses `pow(v, 2.2)` instead
of the piecewise sRGB inverse transfer function. It then converts to D65 XYZ
without normalizing X/Y/Z to a reference white and without D65-to-D50 chromatic
adaptation before standard Lab. As a simple invariant, encoded sRGB white
should be neutral Lab; the current function returns approximately
`L*=100, a*=-8.3, b*=-5.7` rather than `a*=b*=0`.

W3C CSS Color 4 provides standards-derived reference code for the piecewise
sRGB transfer, the sRGB matrix, Bradford D65-to-D50 adaptation, white
normalization, and XYZ-to-Lab conversion.[^css-color] The CIEDE2000 body at
[`color.go:20-127`](../../internal/optimize/color.go#L20) appears structurally
consistent with the published formula, but that cannot rescue invalid Lab
inputs. Validate it independently with the full Sharma-Wu-Dalal supplemental
test pairs, including the difficult hue-wrap cases.[^ciede-cie] [^ciede-sharma]

[`toRGBA`](../../internal/optimize/effects.go#L10) also claims that drawing into
RGBA flattens color profiles into an sRGB-like space. It does not. The standard
Go JPEG/PNG decoders do not perform ICC transforms; upstream PNG tests/source
explicitly ignore `gAMA`, and JPEG APP metadata is not color-converted. A
tagged Display-P3 or Adobe RGB image is processed as if its byte values were
already the working encoding, then JPEG encoding drops the source profile.
PNG 3 defines color-signaling precedence and expects color-aware applications
to use it; ICC.1 defines profile-based transforms.[^go-png] [^go-jpeg-reader]
[^png3] [^icc]

Best practice is: detect source encoding/profile, convert to one documented
working/output space (sRGB is the pragmatic TV-safe default), perform spatial
operations in a linear-light higher-precision buffer where appropriate, encode
the output transfer function, and embed or declare the output space. If adding
ICC support is out of scope, detect and warn/reject unsupported profiles rather
than calling the current conversion normalization.

### 5. Transform caching can preserve stale or wrongly oriented output

The fast path trusts only the `_opt.h_` marker and dimensions
([`resize.go:126-142`](../../internal/optimize/resize.go#L126)); the generated
name also contains only stem, dimensions, digest, and extension
([`identity.go:119-132`](../../internal/artwork/identity.go#L119)). It does not
identify smart-crop mode, portrait mode, museum settings, quality, color
pipeline, or algorithm version. Changing any of those can leave old output in
place indefinitely.

There is a second orientation-specific defect. Width and height are read before
orientation, and an exact-target JPEG with museum mode off skips rewrite
([`resize.go:75-103`](../../internal/optimize/resize.go#L75)). Orientation is
read and applied only inside rewrite
([`resize_rewrite.go:42-59`](../../internal/optimize/resize_rewrite.go#L42)). A
physical 3840x2160 JPEG tagged Exif orientation 6 can therefore be renamed as
optimized even though its displayed orientation is 2160x3840. The same skip
also means exact-size input receives no sharpening/dither/color effects while
resized input does.

Use a versioned transform fingerprint derived from every byte-affecting input,
including output dimensions/format/quality, orientation policy, crop/pad mode,
smart-crop algorithm version, museum parameters, and color pipeline version.
Preflight must inspect orientation before declaring dimensions final. Avoid
repeat JPEG encoding when the canonical pixels and declared transform really
are already correct.

`Config.NormalizeLuminance` is set in
[`DefaultConfig`](../../internal/optimize/resize.go#L38) but is never read and
is not mapped from application configuration. It is dead API, not a working
normalization option.

### 6. Resize is high quality in kernel choice, but not in color domain

Catmull-Rom at [`effects.go:28-30`](../../internal/optimize/effects.go#L28) is a
defensible high-quality cubic interpolator; upstream `x/image/draw` describes
it as a very-high-quality Mitchell-Netravali-family kernel.[^ximage-draw]
The padding path's blurred background plus sharp foreground is a reasonable
stylistic construction.

All resize, blur, sharpening, and texture operations nevertheless act on
gamma-encoded 8-bit RGBA. Resampling encoded sRGB is not radiometrically
correct and can darken high-contrast transitions. The preferred pipeline is
profile-normalized pixels to linear/high precision, crop/resize, bounded
sharpen, output transfer/quantization, then encode. Negative-lobe cubic filters
can ring, so regression images should include hard black/white edges and
saturated line art. ImageMagick's upstream technical guidance documents both
linear-light resize and controlled post-resize sharpening, including the halo
tradeoff.[^im-resize] [^im-sharpen]

### 7. Sharpen and dither should not be unconditional defaults

[`sharpenWithWorkers`](../../internal/optimize/effects.go#L139) applies the
full-strength kernel `center*5 - north - south - east - west` independently to
encoded R/G/B and hard-clips. It has no radius, amount, scale, or threshold and
can amplify JPEG blocks/noise and create colored halos. Prefer a conservative,
scale-aware unsharp mask on luminance/lightness, with an edge threshold and
golden regression images. Skip sharpening when no resampling occurred unless
explicitly requested.

[`ditherWithWorkers`](../../internal/optimize/effects.go#L65) adds the same
random -1/0/+1 value to all three channels after the image is already 8-bit.
That is not quantization-error dithering. It is then passed through lossy JPEG
DCT and fixed 4:2:0 chroma subsampling, which may erase it while reducing
compressibility or exposing noise. Remove it from the JPEG path. If a future
higher-precision working buffer is reduced to 8-bit, dither once at that final
quantization with a defined ordered, error-diffusion, or blue-noise method and
measure banding versus noise.[^ulichney] [^im-quantize]

Go's encoder always writes baseline 8-bit 4:2:0 JPEG; quality controls the
quantization tables, not chroma mode or progressive encoding. This is broadly
compatible with the TV, but saturated fine lines, text, and artwork should be
included in visual/metric tests, and already-compressed inputs should not be
re-encoded repeatedly.[^go-jpeg] [^jpeg-standard]

### 8. Museum mode is an artistic look, not normalization or gamut mapping

The effect is deterministic and opt-in, which is good. Its technical names and
some behavior are misleading:

- The target RMS logic at
  [`museum.go:46-60`](../../internal/optimize/museum.go#L46) appears
  directionally reversed. Low RMS produces an exponent below 1, which tends to
  brighten/compress encoded values; high RMS produces an exponent above 1,
  which tends to darken/spread them. Synthetic ramps/swatches should verify
  that output RMS actually moves toward, not away from, the target.
- RMS is computed from gamma-coded BT.601-like luma, not linear luminance
  ([`museum.go:118-140`](../../internal/optimize/museum.go#L118)).
- The transfer LUT is a power 2.2/1/2.2 approximation rather than sRGB
  ([`museum.go:190-207`](../../internal/optimize/museum.go#L190)).
- "Gamut compression" merely mixes every RGB triplet 3% toward its arithmetic
  mean ([`museum.go:143-178`](../../internal/optimize/museum.go#L143)). It knows
  no source or target gamut and does not test whether a color is out of gamut.
- The final polish independently clips channels to 235, mixes 8% gray, and
  adds more random noise
  ([`museum_polish.go:10-34`](../../internal/optimize/museum_polish.go#L10)).
  Per-channel clipping can alter hue and destroy highlight detail.

Real gamut/tone mapping is target-color-space/display aware and preserves an
in-gamut zone of trust. ACES 2, for example, explicitly separates lightness
tone scale, chroma compression, gamut compression, and display encoding.[^aces]
Keep the canvas/craquelure/weave operations as a creative preset if desired,
but call them a creative look, document clipping/desaturation, and do not
describe them as color normalization or standards-based gamut handling.

### 9. Orientation support is mostly sound but incomplete

The eight JPEG Exif pixel transforms appear correct for zero-origin decoder
images, and re-encoding canonicalizes the chosen orientation. The parser is
bounded and handles little- and big-endian TIFF data. Current Exif is CIPA Exif
3.1 (January 2026). Orientation tag 0x0112 is SHORT, count 1, values 1 through
8; the repository also accepts LONG and does not validate count/range. Parse
errors are silently ignored in rewrite and collage, which can silently
misorient malformed files.[^exif31]

PNG 3 standardizes an `eXIf` chunk, including orientation. The collection and
collage paths accept PNG, but `ReadOrientation` is JPEG-only. Add PNG orientation
if PNG remains a supported input, and log a structured warning when orientation
metadata is malformed. The rotation helpers also assume zero-origin bounds;
make that precondition explicit or account for `Bounds().Min`.

### 10. Collage and PNG behavior are inconsistent

[`CreateCollage`](../../internal/optimize/collage.go#L9) always builds
3840x2160, while the output name records `cfg.MaxWidth` and `cfg.MaxHeight`
([`collage_pipeline.go:133-136`](../../internal/optimize/collage_pipeline.go#L133)).
Any non-default target yields an incorrect filename contract. The output format
is selected from the first input's extension
([`collage_pipeline.go:125-150`](../../internal/optimize/collage_pipeline.go#L125)),
which makes mixed JPEG/PNG pairs format-order-dependent and leaves alpha/color
profile behavior undefined.

The main optimizer explicitly skips non-JPEG inputs
([`resize.go:63-67`](../../internal/optimize/resize.go#L63)), even though PNG is
accepted, cataloged, uploaded, and used by collage. Decide whether PNG is a
pass-through source or an optimizable input and document that contract. If it
is processed, address profile, gamma, alpha flattening, eXIf, and output format
explicitly.

The collage transaction order is sound: it publishes the durable output before
deleting either source and propagates deletion errors
([`collage_pipeline.go:167-181`](../../internal/optimize/collage_pipeline.go#L167)).
Portrait pairing is deterministic because inputs are sorted before pairing.

### 11. Validation is strong at ingress but weak at output

[`collection.readAndValidate`](../../internal/collection/validate.go#L27) is a
strong untrusted-input boundary: bounded encoded bytes, `DecodeConfig`, an
overflow-safe pixel limit, full decode, format/dimension consistency, and
SHA-256. Download validation similarly uses a 40-million-pixel cap and full
decode ([`loader_download.go:123-156`](../../internal/sources/loader_download.go#L123)).
This matches Go's security guidance to inspect configuration before allocating
for full decode.[^go-image-security]

By contrast, optimizer [`ValidateImage`](../../internal/optimize/resize.go#L188)
calls only `DecodeConfig` while claiming a corruption check. `DecodeConfig`
validates enough header data to return type/dimensions, not the complete pixel
stream. Full-decode the produced temporary file, verify expected format and
dimensions, and only then publish it. The optimizer's fast path already does a
full decode; output publication should provide the same assurance.

Context cancellation is checked between large phases but cannot interrupt the
standard decoder or long pixel loops. The byte/pixel caps limit exposure; if
latency under cancellation matters, make custom loops cancellation-aware at
row/chunk boundaries and document that `image.Decode` itself is not
interruptible.

### 12. Deduplication and catalog identity are safe in intent, limited in scope

Exact SHA-256 is appropriate for byte identity and stable content-addressed
naming.[^sha256] Deterministic sorting and canonical collection hashing are
also good. It deliberately does not detect visually identical files with
different metadata, quality, orientation representation, or encoding. That is
not a SHA-256 defect. If near-duplicate discovery is wanted, add a secondary
perceptual fingerprint for reporting/review only; do not automatically delete
based on it because perceptual hashes have false positives and adversarial
collisions. Preserve exact digest as the authoritative mutation key.

Catalog rebuild rejects directory entries initially identified as symlinks or
non-regular files
([`catalog_index.go:53-59`](../../internal/sources/catalog_index.go#L53)), then
later opens each path by name to hash it
([`catalog_index.go:119-131`](../../internal/sources/catalog_index.go#L119)). A
replacement between those steps can change the type/content being hashed. Open
the file without following symlinks where supported, `fstat` the opened handle,
hash that handle, and optionally verify name-to-inode stability before accepting
the result.

Filename parsing is also looser than its comments. `fmt.Sscanf("%dx%d")`
accepts a valid prefix and does not anchor the entire segment
([`identity.go:27-46`](../../internal/artwork/identity.go#L27));
`StripIndexPrefix` removes any short prefix before `__`, not only digits
([`identity.go:85-90`](../../internal/artwork/identity.go#L85)). Use an anchored
canonical parser so malformed names cannot authorize the optimizer fast path or
collapse identities.

## README and operator-documentation audit

The documentation was checked against the actual processing branches, not just
the intended design. These statements should be corrected together with the
corresponding behavior changes:

| Documentation claim | Runtime behavior | Required clarification |
| --- | --- | --- |
| [`README.md`](../../README.md) describes `IMAGE_MAX_WIDTH` and `IMAGE_MAX_HEIGHT` as maxima and says larger images are resized | Every rewritten JPEG is forced to the exact configured dimensions; smaller images are upscaled | Call them target dimensions, or change the algorithm to true maximum/fits-within behavior |
| README broadly says JPEG and PNG artwork is optimized | Individual PNGs pass through without resize, smart crop, sharpen, dither, museum effects, or Exif orientation | State the JPEG-only transform contract or implement explicit PNG processing |
| README implies `PORTRAIT_MODE` governs portrait handling uniformly | Files whose names start with `upload` are collage candidates regardless of `PORTRAIT_MODE`; an odd upload portrait waits unprocessed for a partner | Document the upload exception or make the setting authoritative |
| README calls smart crop content-aware; config comments call it entropy analysis | The implementation uses fixed BMS/Sobel/skin/color/composition heuristics; there is no entropy algorithm, and BMS is currently broken | Name the actual algorithm and avoid quality claims until benchmarked |
| README calls SHA-256 a content hash in deduplication prose | It hashes the exact encoded bytes, so metadata or a JPEG re-encode changes identity even when displayed pixels look the same | Say exact-file or encoded-byte deduplication; keep it authoritative |
| [`docs/apple-photos-sync.md`](../apple-photos-sync.md) says every uploaded JPEG/PNG is optimized to 4K | PNG and unpaired upload portraits can remain unprocessed | Align the guide with the real JPEG/portrait contract |
| The Apple Photos guide says the upload endpoint accepts files without authentication and its `curl` examples omit credentials | Current configuration requires an upload token whenever uploads are enabled, and the handler uses Basic authentication | Remove the unauthenticated claim and add `-u frame:$UPLOAD_TOKEN` (or the actual configured username contract) to examples |
| The Apple Photos guide describes optimizer framing as a matte | Matte selection is TV/display state, separate from pixels created by crop, pad, or collage | Separate image geometry from TV matte settings |

No README change was made during this audit because several corrections depend
on the implementation policy choices above. The authentication examples are a
standalone documentation defect and can be corrected without changing image
behavior.

## Components that are already sound

- The summed-area/integral-image recurrence and inclusive rectangle lookup are
  mathematically consistent, and the sliding window uses the same convention.
- Catmull-Rom is a legitimate high-quality resize kernel; retain it initially
  while fixing the working color domain and tests.
- All eight JPEG Exif orientation transforms are represented. Tighten metadata
  conformance and preflight rather than replacing the transform mapping.
- Ingress validation follows the correct bounded-header-then-full-decode
  pattern and checks type/dimension consistency.
- Exact SHA-256 deduplication, deterministic naming/pairing, and canonical
  collection hashing are suitable for authoritative byte identity.
- Transactional output publication and collage's publish-before-delete order
  preserve last-known-good artwork on failure.
- Control-file exclusion and dry-run/durability invariants are orthogonal to
  the image math and remain important strengths.

## Recommended implementation sequence

1. **Stop correctness failures:** replace solar math with exact referenced
   equations and golden vectors; repair orientation-aware preflight; include a
   transform fingerprint.
2. **Create mathematical conformance tests:** BMS topology maps, sRGB/Lab
   primary/neutral values, all Sharma CIEDE2000 pairs, Exif 1-8, PNG eXIf if
   supported, and full-stream truncation tests.
3. **Create a crop benchmark before changing weights:** representative Frame TV
   art, portraits across skin tones/lighting, animals, text/line art,
   landscapes, off-center subjects, multiple subjects, uniform/low-contrast
   images, distractors at edges, and already-good compositions. Compare smart
   crop with center crop and require conservative fallback on low confidence.
4. **Repair or replace smart crop:** faithful BMS or a clearly named simpler
   heuristic, antialiased analysis, normalized features, centered ties,
   subject-boundary penalty/margin, and ablation measurements. Keep model-based
   cropping optional/out of process if explored; do not burden the core engine
   without measured benefit.
5. **Establish the color pipeline:** detect/profile-convert to declared sRGB,
   orient, use a linear/high-precision working buffer for spatial operations,
   transfer/quantize once, and encode with declared output semantics.
6. **Reduce destructive defaults:** replace full-strength sharpen with bounded
   scale-aware unsharp masking and remove random dither from JPEG. Avoid
   repeat recompression.
7. **Clarify artistic and format contracts:** relabel museum mode as a creative
   look, fix/measure its contrast direction, parameterize collage size, and
   explicitly decide PNG/profile/alpha/orientation behavior.
8. **Harden output/catalog validation:** full-decode produced files and close
   the catalog open/stat race. Add structured warnings for unsupported or
   malformed metadata.

## Primary sources

[^noaa-details]: NOAA Global Monitoring Laboratory, [Solar calculation details and spreadsheets](https://gml.noaa.gov/grad/solcalc/calcdetails.html) and [General Solar Position Calculations](https://gml.noaa.gov/grad/solcalc/solareqns.PDF).
[^nrel-spa]: Reda and Andreas, NREL, [Solar Position Algorithm for Solar Radiation Applications](https://docs.nrel.gov/docs/fy08osti/34302.pdf).
[^kasten-young]: Kasten and Young, [Revised optical air mass tables and approximation formula](https://pubmed.ncbi.nlm.nih.gov/20555942/), *Applied Optics* 28(22), 1989, DOI 10.1364/AO.28.004735.
[^bms]: Zhang and Sclaroff, [Saliency Detection: A Boolean Map Approach](https://openaccess.thecvf.com/content_iccv_2013/papers/Zhang_Saliency_Detection_A_2013_ICCV_paper.pdf), ICCV 2013.
[^ximage-draw]: Go project, [`golang.org/x/image/draw` package documentation](https://pkg.go.dev/golang.org/x/image/draw).
[^skin-cvprw]: Xu et al., [Color Invariant Skin Segmentation](https://openaccess.thecvf.com/content/CVPR2022W/FaDE-TCV/papers/Xu_Color_Invariant_Skin_Segmentation_CVPRW_2022_paper.pdf), CVPR Workshops 2022.
[^venus]: Du et al., [Venus: Benchmarking and Empowering Multimodal Large Language Models for Aesthetic Image Cropping](https://openaccess.thecvf.com/content/CVPR2026/html/Du_Venus_Benchmarking_and_Empowering_Multimodal_Large_Language_Models_for_Aesthetic_CVPR_2026_paper.html), CVPR 2026.
[^photoframer]: You et al., [PhotoFramer: Multi-modal Image Composition Instruction](https://openaccess.thecvf.com/content/CVPR2026/html/You_PhotoFramer_Multi-modal_Image_Composition_Instruction_CVPR_2026_paper.html), CVPR 2026.
[^spatial-rank]: Wang et al., [Image Cropping With Spatial-Aware Feature and Rank Consistency](https://openaccess.thecvf.com/content/CVPR2023/papers/Wang_Image_Cropping_With_Spatial-Aware_Feature_and_Rank_Consistency_CVPR_2023_paper.pdf), CVPR 2023.
[^crop-discard]: Cheng et al., [Re-Compose the Image by Evaluating the Crop on More Than Just a Score](https://openaccess.thecvf.com/content/WACV2022/papers/Cheng_Re-Compose_the_Image_by_Evaluating_the_Crop_on_More_Than_WACV_2022_paper.pdf), WACV 2022.
[^mit-saliency]: MIT, [Saliency Benchmark](https://saliency.mit.edu/) and [benchmark results](https://saliency.mit.edu/results.html).
[^center-bias]: Borji, Sihite, and Itti, [What/Where to Look Next? Modeling Top-Down Visual Attention in Complex Interactive Environments](https://openaccess.thecvf.com/content_iccv_2013/papers/Borji_Analysis_of_Scores_2013_ICCV_paper.pdf), ICCV 2013 analysis of saliency scores and center bias.
[^css-color]: W3C, [CSS Color Module Level 4: Sample code for color conversions](https://www.w3.org/TR/css-color-4/#color-conversion-code).
[^ciede-cie]: CIE, [Colorimetry Part 6: CIEDE2000 Colour-Difference Formula](https://www.cie.co.at/publications/colorimetry-part-6-ciede2000-colour-difference-formula-1).
[^ciede-sharma]: Sharma, Wu, and Dalal, [The CIEDE2000 Color-Difference Formula: Implementation Notes, Supplementary Test Data, and Mathematical Observations](https://www.ece.rochester.edu/~gsharma/ciede2000/ciede2000noteCRNA.pdf), *Color Research & Application* 30(1), 2005.
[^go-png]: Go project, [`image/png` decoder source](https://go.dev/src/image/png/reader.go) and [tests documenting ignored gAMA](https://go.dev/src/image/png/reader_test.go).
[^go-jpeg-reader]: Go project, [`image/jpeg` decoder source](https://go.dev/src/image/jpeg/reader.go).
[^png3]: W3C, [Portable Network Graphics (PNG) Specification, Third Edition](https://www.w3.org/TR/png-3/).
[^icc]: International Color Consortium, [ICC.1:2022 Profile Format Specification](https://www.color.org/specifications/ICC.1-2022-05.pdf).
[^im-resize]: ImageMagick, [Resizing Images](https://usage.imagemagick.org/resize/) and [Color Management](https://imagemagick.org/color-management/).
[^im-sharpen]: ImageMagick, [Sharpening and unsharp masking](https://usage.imagemagick.org/blur/#sharpen).
[^ulichney]: Ulichney, [Dithering with Blue Noise](https://doi.org/10.1109/5.3288), *Proceedings of the IEEE* 76(1), 1988.
[^im-quantize]: ImageMagick, [Color Quantization and Dithering](https://usage.imagemagick.org/quantize/).
[^go-jpeg]: Go project, [`image/jpeg` documentation](https://pkg.go.dev/image/jpeg) and [encoder source](https://go.dev/src/image/jpeg/writer.go).
[^jpeg-standard]: ITU-T, [Recommendation T.81: Digital compression and coding of continuous-tone still images](https://www.itu.int/rec/T-REC-T.81).
[^aces]: Academy Software Foundation, [ACES 2 Output Transforms](https://docs.acescentral.com/system-components/output-transforms/) and [Gamut Compression](https://docs.acescentral.com/system-components/output-transforms/technical-details/gamut-compression/).
[^exif31]: CIPA, [Exif 3.1 and current digital-camera standards](https://www.cipa.jp/e/std/std-sec.html).
[^go-image-security]: Go project, [`image` package security considerations](https://pkg.go.dev/image#hdr-Security_Considerations).
[^sha256]: NIST, [FIPS 180-4 Secure Hash Standard](https://csrc.nist.gov/files/pubs/fips/180-4/final/docs/fips180-4.pdf).
