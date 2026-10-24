package optimize

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
)

// catalogPass carries the dependencies and outcome of one optimization pass
// over the artwork files named by inputs inside artworkDir.
type catalogPass struct {
	artworkDir string
	cfg        Config
	logger     *slog.Logger
	inputs     map[string]StageInput
	optimized  int
	renames    []Rename
}

// run pairs portrait collages, then transforms every remaining file. It
// returns collage failures and cancellation; individual files that cannot be
// transformed are logged and left in place.
func (p *catalogPass) run(ctx context.Context) error {
	localFiles := make(map[string]struct{}, len(p.inputs))
	for name := range p.inputs {
		// AppleDouble ("._") sidecars are never artwork.
		if !strings.HasPrefix(name, "._") {
			localFiles[name] = struct{}{}
		}
	}
	// Collage pairing is explicit: every portrait source is eligible only when
	// PORTRAIT_MODE=collage. Upload origin does not override operator policy.
	collageErr := p.processCollages(ctx, localFiles)

	// Full-resolution decodes are serialized, avoiding multiple 4K image
	// buffers in memory; pixel-level work inside each transform is parallel.
	for filename := range localFiles {
		if ctx.Err() != nil {
			break
		}
		p.optimizeFile(ctx, filename)
	}
	return errors.Join(ctx.Err(), collageErr)
}

// optimizeFile transforms one file in place, recording its rename. Files that
// fail are logged and skipped.
func (p *catalogPass) optimizeFile(ctx context.Context, filename string) {
	path := filepath.Join(p.artworkDir, filename)
	if !p.cfg.Enabled {
		if err := ValidateImage(ctx, path); err != nil {
			p.logger.Warn("skipping corrupt image", "file", filename, "error", err)
		}
		return
	}

	input := p.inputs[filename]
	transformKey := TransformKey(p.cfg)
	if input.Derivative != "" && input.TransformKey == transformKey &&
		input.Width == p.cfg.MaxWidth && input.Height == p.cfg.MaxHeight {
		return
	}
	force := input.Derivative != "" && input.TransformKey != transformKey
	newFilename, modified, err := optimizeFileWithPolicy(ctx, path, cmp.Or(input.Key, filename), force, p.cfg, p.logger)
	if err != nil {
		p.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		return
	}
	if modified {
		p.optimized++
		if newFilename != filename {
			p.renames = append(p.renames, Rename{OldName: filename, NewName: newFilename})
		}
	}
}
