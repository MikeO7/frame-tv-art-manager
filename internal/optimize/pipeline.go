package optimize

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// catalogPass carries the dependencies and outcome of one optimization pass
// over the artwork files named by inputs inside artworkDir. Kept originals are
// copied into originalsDir only when a derivative must be rendered again.
type catalogPass struct {
	artworkDir   string
	originalsDir string
	cfg          Config
	logger       *slog.Logger
	// cropClient calls the advanced crop provider for every picture of the
	// pass; run closes its idle connections when the pass ends.
	cropClient *http.Client
	inputs     map[string]StageInput
	optimized  int
	renames    []rename
	// lineage records, by output name, the digests of the originals each
	// output was rendered from, in render order.
	lineage map[string][][sha256.Size]byte
	staged  map[[sha256.Size]byte]string
	// decoded holds, by path, portraits decoded to measure them for pairing,
	// kept for the collage that renders them next. See keepDecoded.
	decoded map[string]*image.RGBA
}

// run pairs portrait collages, then transforms every remaining file. It
// returns collage failures and cancellation; individual files that cannot be
// transformed are logged and left in place.
func (p *catalogPass) run(ctx context.Context) error {
	defer p.env().close()
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
		// A collage rendered this pass is complete: transforming it again
		// would stack effects on it and replace its lineage.
		if _, input := p.inputs[filename]; !input {
			continue
		}
		p.optimizeFile(ctx, filename)
	}
	return errors.Join(ctx.Err(), collageErr)
}

// env is what the pass renders every picture with.
func (p *catalogPass) env() renderEnv {
	return renderEnv{cfg: p.cfg, logger: p.logger, cropClient: p.cropClient}
}

// optimizeFile transforms one file in place, recording its rename. A
// derivative is always rendered again from its originals, never from its own
// transformed bytes, so settings changes never stack. Files that fail are
// logged and skipped.
func (p *catalogPass) optimizeFile(ctx context.Context, filename string) {
	path := filepath.Join(p.artworkDir, filename)
	if !p.cfg.Enabled {
		if err := validateImage(ctx, path); err != nil {
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
	sources, lineage, err := p.renderSources(ctx, filename, input)
	if err != nil {
		p.logger.Warn("skipping artwork whose original cannot be read", "file", filename, "error", err)
		return
	}
	label := cmp.Or(input.Key, filename)
	var newFilename string
	modified := true
	if len(sources) == 2 {
		newFilename, err = p.rerenderCollage(ctx, filename, sources, label)
	} else {
		newFilename, modified, err = optimizeFileWithPolicy(ctx, fileTransform{
			source: sources[0], path: path, label: label, force: input.Derivative != "",
		}, p.env())
	}
	if err != nil {
		p.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		return
	}
	if modified {
		p.optimized++
		// A derivative rendered again to the same bytes keeps its name, and
		// is still recorded so the collection stores the new transform key.
		// Otherwise it would be rendered again on every pass.
		if newFilename != filename || input.Derivative != "" {
			p.renames = append(p.renames, rename{OldName: filename, NewName: newFilename})
			p.recordLineage(newFilename, lineage...)
		}
	}
}

// renderSources returns the files filename is rendered from and their
// digests. Raw artwork, and derivatives made before originals were kept,
// render from their own staged bytes, which become their original. A kept
// original that is gone or no longer matches its digest cannot be recovered,
// so the current bytes are the best available original; any other failure
// skips the file this pass instead of silently giving up its lineage.
func (p *catalogPass) renderSources(
	ctx context.Context,
	filename string,
	input StageInput,
) ([]string, [][sha256.Size]byte, error) {
	own := []string{filepath.Join(p.artworkDir, filename)}
	ownLineage := [][sha256.Size]byte{input.Digest}
	if input.Derivative == "" || len(input.Originals) == 0 {
		return own, ownLineage, nil
	}
	sources := make([]string, 0, len(input.Originals))
	lineage := make([][sha256.Size]byte, 0, len(input.Originals))
	for _, original := range input.Originals {
		path, err := p.stageOriginal(ctx, original)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errStageDigestMismatch) {
			p.logger.Warn("kept original is unusable; keeping the current file as its original",
				"file", filename, "error", err)
			return own, ownLineage, nil
		}
		if err != nil {
			return nil, nil, err
		}
		sources = append(sources, path)
		lineage = append(lineage, original.Digest)
	}
	return sources, lineage, nil
}

// stageOriginal copies one kept original into the private workspace, verifying
// its digest, so the transform never reads the collection's control files.
func (p *catalogPass) stageOriginal(ctx context.Context, original StageOriginal) (string, error) {
	if path, staged := p.staged[original.Digest]; staged {
		return path, nil
	}
	if p.originalsDir == "" {
		return "", errors.New("optimization pass has no originals workspace")
	}
	name := hex.EncodeToString(original.Digest[:]) + strings.ToLower(filepath.Ext(original.Path))
	input := StageInput{Name: name, Path: original.Path, Digest: original.Digest}
	if err := copyStageInput(ctx, p.originalsDir, input); err != nil {
		path := filepath.Join(p.originalsDir, name)
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return "", errors.Join(err, fmt.Errorf("remove partial staged original: %w", removeErr))
		}
		return "", err
	}
	if p.staged == nil {
		p.staged = make(map[[sha256.Size]byte]string)
	}
	p.staged[original.Digest] = filepath.Join(p.originalsDir, name)
	return p.staged[original.Digest], nil
}

func (p *catalogPass) recordLineage(name string, digests ...[sha256.Size]byte) {
	if p.lineage == nil {
		p.lineage = make(map[string][][sha256.Size]byte)
	}
	p.lineage[name] = digests
}
