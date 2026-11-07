package assistant

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyInput struct{}

type displaySpec struct {
	Width                 int        `json:"width" jsonschema:"exact width in pixels of the artwork the TV displays"`
	Height                int        `json:"height" jsonschema:"exact height in pixels of the artwork the TV displays"`
	AspectRatio           string     `json:"aspect_ratio" jsonschema:"display aspect ratio"`
	ColorSpace            string     `json:"color_space" jsonschema:"color space images are converted to"`
	Formats               []string   `json:"formats" jsonschema:"accepted image formats"`
	MaxImageBytes         int64      `json:"max_image_bytes" jsonschema:"largest accepted image file in bytes"`
	MaxBase64Bytes        int64      `json:"max_base64_characters" jsonschema:"approximate largest image_base64 value accepted"`
	RecommendedGeneration string     `json:"recommended_generation" jsonschema:"how to size generated images"`
	Fitting               fitPolicy  `json:"fitting" jsonschema:"how images that are not exactly the display size are placed"`
	PromptTips            []string   `json:"prompt_tips" jsonschema:"advice for prompts that look like real art on the matte screen"`
	Workflow              []string   `json:"workflow" jsonschema:"the tool sequence for building a collection"`
	DryRun                bool       `json:"dry_run" jsonschema:"true when changes are not saved"`
	Display               displayEnv `json:"display" jsonschema:"facts about the physical screen"`
}

type fitPolicy struct {
	OptimizationEnabled bool    `json:"optimization_enabled"`
	MaxCropPercent      int     `json:"max_crop_percent"`
	MaxUpscale          float64 `json:"max_upscale"`
	PortraitMode        string  `json:"portrait_mode"`
	PadStyle            string  `json:"pad_style"`
	MatTone             string  `json:"mat_tone"`
	Summary             string  `json:"summary"`
}

type displayEnv struct {
	Screen  string `json:"screen"`
	Matte   string `json:"matte"`
	Ambient string `json:"ambient"`
}

func (svc *service) registerSpecTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:  "get_display_spec",
		Title: "Get the TV display specification",
		Description: "Returns the exact image specification for the Samsung Frame TV: 3840x2160 pixels, 16:9, sRGB, " +
			"JPEG or PNG, the upload size limit, how images of other shapes are cropped or placed on a gallery mat " +
			"with the current settings, and prompt tips for art that looks real on the matte screen. Call this " +
			"before generating images.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	}, func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, displaySpec, error) {
		return nil, svc.displaySpec(), nil
	})
}

func (svc *service) displaySpec() displaySpec {
	cfg := svc.cfg
	return displaySpec{
		Width: cfg.OptimizeMaxWidth, Height: cfg.OptimizeMaxHeight, AspectRatio: aspectRatio(cfg.OptimizeMaxWidth, cfg.OptimizeMaxHeight),
		ColorSpace: "sRGB", Formats: []string{"JPEG", "PNG"},
		MaxImageBytes: svc.maxImageBytes(), MaxBase64Bytes: svc.maxImageBytes() * 4 / 3,
		RecommendedGeneration: fmt.Sprintf(
			"Generate landscape %s images at the highest resolution your model supports, ideally exactly %dx%d. "+
				"Anything smaller than about %dx%d is visibly enlarged on a 4K screen. Prefer JPEG at high quality; "+
				"PNG is accepted.",
			aspectRatio(cfg.OptimizeMaxWidth, cfg.OptimizeMaxHeight), cfg.OptimizeMaxWidth, cfg.OptimizeMaxHeight,
			cfg.OptimizeMaxWidth*2/3, cfg.OptimizeMaxHeight*2/3),
		Fitting: fitPolicy{
			OptimizationEnabled: cfg.OptimizeEnabled, MaxCropPercent: cfg.MaxCropPercent, MaxUpscale: cfg.MaxUpscale,
			PortraitMode: cfg.PortraitMode, PadStyle: cfg.PadStyle, MatTone: cfg.MatTone,
			Summary: svc.fitSummary(),
		},
		PromptTips: []string{
			"Ask for a 16:9 landscape composition and keep the main subject away from the edges.",
			"Painterly, textured styles (oil, gouache, watercolor, etching, woodcut) look most like real art on the " +
				"matte screen; glossy photoreal renders look like a TV.",
			"Prefer muted, natural palettes and soft lighting. Avoid large pure-white or neon areas: they glow in a dark room at night.",
			"Do not include text, signatures, watermarks, borders, frames, or mats; the TV adds its own mat and frame.",
			"For a set, keep one style and palette across all images so the collection feels curated.",
		},
		Workflow: []string{
			"get_display_spec", "generate images with your own image tool", "add_artwork (one call per image, same collection)",
			"preview_artwork", "update_collection (mode window with MM-DD dates, or always)", "sync_now", "get_status",
		},
		DryRun: cfg.DryRun,
		Display: displayEnv{
			Screen:  "Samsung The Frame, 4K UHD, Art Mode",
			Matte:   "anti-reflection matte panel: fine detail and texture read well, deep blacks are lifted slightly",
			Ambient: "brightness follows the room or a schedule, so mid-tones carry the image",
		},
	}
}

func (svc *service) fitSummary() string {
	cfg := svc.cfg
	if !cfg.OptimizeEnabled {
		return "Image optimization is off: the TV receives each file unchanged, so send exactly the display size."
	}
	portrait := "Portrait images are shown whole on a mat."
	switch cfg.PortraitMode {
	case "crop":
		portrait = "Portrait images are cropped to fill the screen."
	case "collage":
		portrait = "Portrait images are paired side by side with another portrait when one is available."
	}
	pad := "a gallery mat"
	if cfg.PadStyle == "blur" {
		pad = "a blurred copy of the image"
	}
	return fmt.Sprintf(
		"Landscape images are cropped to fill the screen when that removes at most %d%% of the picture and needs "+
			"at most %.3gx enlargement; otherwise they are shown whole on %s. %s",
		cfg.MaxCropPercent, cfg.MaxUpscale, pad, portrait)
}

func aspectRatio(width, height int) string {
	if width <= 0 || height <= 0 {
		return "unknown"
	}
	divisor := gcd(width, height)
	return fmt.Sprintf("%d:%d", width/divisor, height/divisor)
}

func gcd(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}
