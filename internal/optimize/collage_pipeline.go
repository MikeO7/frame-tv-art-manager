package optimize

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"maps"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

// processCollagePair fuses two portrait files into one landscape collage,
// publishes it, and removes both sources. Their bytes become the collage's
// originals, left then right.
func (p *catalogPass) processCollagePair(ctx context.Context, f1, f2 string) (string, error) {
	p1 := filepath.Join(p.artworkDir, f1)
	p2 := filepath.Join(p.artworkDir, f2)
	collageName, err := p.renderCollageFile(ctx, p1, p2, "collage-"+f1+"-"+f2, collageOutputExtension(f1, f2))
	if err != nil {
		return "", err
	}

	// The durable collage is published before either input is removed, so a
	// crash or deletion failure always leaves at least one complete artwork.
	if err := durablefs.Remove(ctx, p1); err != nil {
		return collageName, fmt.Errorf("remove first collage source: %w", err)
	}
	if err := durablefs.Remove(ctx, p2); err != nil {
		return collageName, fmt.Errorf("remove second collage source: %w", err)
	}

	p.renames = append(p.renames, rename{OldName: f1, NewName: collageName}, rename{OldName: f2, NewName: collageName})
	p.recordLineage(collageName, p.inputs[f1].Digest, p.inputs[f2].Digest)
	return collageName, nil
}

// rerenderCollage renders an existing collage again from its two kept
// originals and replaces it, so collage settings changes never stack.
func (p *catalogPass) rerenderCollage(ctx context.Context, filename string, originals []string, label string) (string, error) {
	path := filepath.Join(p.artworkDir, filename)
	collageName, err := p.renderCollageFile(ctx, originals[0], originals[1], label, filepath.Ext(filename))
	if err != nil || collageName == filename {
		return collageName, err
	}
	if err := durablefs.Remove(ctx, path); err != nil {
		removeErr := os.Remove(filepath.Join(p.artworkDir, collageName))
		return "", errors.Join(fmt.Errorf("replace collage %s: %w", filename, err), removeErr)
	}
	return collageName, nil
}

// renderCollageFile renders the portraits at first and second side by side
// and publishes the result in the workspace under its content name.
func (p *catalogPass) renderCollageFile(ctx context.Context, first, second, label, ext string) (string, error) {
	cfg := p.cfg
	if err := validateOutputDimensions(cfg.MaxWidth, cfg.MaxHeight, cfg.MaxOutputPixels); err != nil {
		return "", err
	}
	// Whatever happens, this pair's decoded portraits are finished with.
	defer p.dropDecoded(first, second)
	inputs, err := inspectCollageInputs(ctx, first, second)
	if err != nil {
		return "", err
	}
	if err := validateWorkingSet(inputs, cfg); err != nil {
		return "", err
	}
	// The estimate covers this pair, decoded or not. Portraits kept for later
	// pairs are on top of it.
	p.releaseDecoded(estimatedWorkingBytes(inputs, cfg), first, second)

	img1, err := p.decodedPortrait(ctx, first)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", filepath.Base(first), err)
	}
	img2, err := p.decodedPortrait(ctx, second)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", filepath.Base(second), err)
	}

	collage := renderCollage(ctx, img1, img2, p.env())
	if cfg.MuseumModeEnabled {
		collage = applyMuseumMode(collage, cfg.MuseumModeIntensity)
	}

	out, err := os.CreateTemp(p.artworkDir, ".collage-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create collage output: %w", err)
	}
	defer func() { _ = os.Remove(out.Name()) }()
	if err := writeValidatedOutput(ctx, out, ext, collage, cfg); err != nil {
		return "", fmt.Errorf("write collage: %w", err)
	}
	collageName, err := moveToContentName(ctx, out.Name(), contentNameRequest{
		directory: p.artworkDir, label: label, extension: ext,
	})
	if err != nil {
		return "", fmt.Errorf("commit collage: %w", err)
	}
	return collageName, nil
}

// decodedPortrait returns the portrait at path decoded, color-converted and
// upright, taking the copy kept from measuring it when there is one so the
// file is not decoded a second time.
func (p *catalogPass) decodedPortrait(ctx context.Context, path string) (*image.RGBA, error) {
	if img, kept := p.decoded[path]; kept {
		delete(p.decoded, path)
		return img, nil
	}
	return loadAndRotateImageWithConfig(ctx, path, p.cfg, p.logger)
}

// keepDecoded keeps a measured portrait for the collage that will render it.
// Kept portraits count against the working-set limit: releaseDecoded makes
// room before every decode and render, so what is kept plus the work at hand
// never exceeds it.
func (p *catalogPass) keepDecoded(path string, img *image.RGBA) {
	if p.decoded == nil {
		p.decoded = make(map[string]*image.RGBA)
	}
	p.decoded[path] = img
}

// decodedBytes is the memory held by kept portraits other than those at the
// excepted paths.
func (p *catalogPass) decodedBytes(except ...string) int64 {
	var held int64
	for path, img := range p.decoded {
		if !slices.Contains(except, path) {
			held += int64(len(img.Pix))
		}
	}
	return held
}

// releaseDecoded drops kept portraits, in path order, until those left and
// needed more bytes of work fit the working-set limit together. The portraits
// at the keep paths are part of that work and stay. A dropped portrait is
// simply decoded again when its collage is rendered.
func (p *catalogPass) releaseDecoded(needed int64, keep ...string) {
	limit := workingByteLimit(p.cfg)
	for _, path := range slices.Sorted(maps.Keys(p.decoded)) {
		if p.decodedBytes(keep...)+needed <= limit {
			return
		}
		if !slices.Contains(keep, path) {
			delete(p.decoded, path)
		}
	}
}

func (p *catalogPass) dropDecoded(paths ...string) {
	for _, path := range paths {
		delete(p.decoded, path)
	}
}

func inspectCollageInputs(ctx context.Context, paths ...string) (workingInput, error) {
	var inputs workingInput
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return workingInput{}, err
		}
		file, err := os.Open(path)
		if err != nil {
			return workingInput{}, fmt.Errorf("open collage input %s: %w", filepath.Base(path), err)
		}
		config, decodeErr := decodeInputConfig(ctx, file)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return workingInput{}, fmt.Errorf("inspect collage input %s: %w", filepath.Base(path), err)
		}
		inputs = inputs.plus(workingInputFor(config))
	}
	return inputs, nil
}

func collageOutputExtension(first, second string) string {
	if strings.EqualFold(filepath.Ext(first), extPNG) || strings.EqualFold(filepath.Ext(second), extPNG) {
		return extPNG
	}
	return extJPG
}

// collectRawPortraits returns, sorted, the un-derived portrait files eligible
// for collage pairing. Only PORTRAIT_MODE=collage pairs portraits.
func (p *catalogPass) collectRawPortraits(ctx context.Context, localFiles map[string]struct{}) []string {
	if p.cfg.PortraitMode != portraitModeCollage {
		return nil
	}
	var rawPortraits []string
	for filename := range localFiles {
		if p.inputs[filename].Derivative != "" {
			continue
		}
		path := filepath.Join(p.artworkDir, filename)
		if isPortrait, err := isPortraitFile(ctx, path); err != nil || !isPortrait {
			continue
		}
		// A portrait too large to transform alone cannot be rendered into any
		// collage. Leaving it out keeps it from taking a partner that another
		// portrait could use, and matches what RequiresStage expects.
		input, err := inspectCollageInputs(ctx, path)
		if err == nil {
			err = validateWorkingSet(input, p.cfg)
		}
		if err != nil {
			p.logger.Debug("portrait cannot be collaged; leaving it as it is", "file", filename, "error", err)
			continue
		}
		rawPortraits = append(rawPortraits, filename)
	}
	// Sorting makes pairing deterministic despite random map iteration order.
	slices.Sort(rawPortraits)
	return rawPortraits
}

// processCollages pairs every eligible raw portrait into collages, replacing
// the pairs with their collage in localFiles.
func (p *catalogPass) processCollages(ctx context.Context, localFiles map[string]struct{}) error {
	rawPortraits := p.collectRawPortraits(ctx, localFiles)
	pairs, leftover := pairPortraits(p.portraitFeatures(ctx, rawPortraits))
	// Every portrait still decoded is released when pairing ends, rendered
	// or not.
	defer clear(p.decoded)
	if leftover != "" {
		p.dropDecoded(filepath.Join(p.artworkDir, leftover))
	}
	var collageErrors []error
	for _, pair := range pairs {
		f1, f2 := pair[0], pair[1]
		p.logger.Info("pairing portrait images into collage", "file1", f1, "file2", f2)
		collageFilename, err := p.processCollagePair(ctx, f1, f2)
		if collageFilename != "" {
			delete(localFiles, f1)
			delete(localFiles, f2)
			localFiles[collageFilename] = struct{}{}
			p.optimized++
		}
		// Two large portraits can exceed the memory budget together though
		// each fits alone. That is a property of the pair, not a failed
		// pass: the pair stays as it is and every other file is still
		// optimized.
		if errors.Is(err, errWorkingSetLimit) {
			p.logger.Warn("portrait pair is too large to collage; leaving both as they are",
				"file1", f1, "file2", f2, "error", err)
			continue
		}
		if err != nil {
			p.logger.Error("failed to create collage pair", "file1", f1, "file2", f2, "error", err)
			collageErrors = append(collageErrors, err)
		}
	}

	// If an odd number remains, leave the unpaired raw input untouched so it can
	// pair with a future upload. Derivative metadata, not its filename,
	// preserves that eligibility across cycles.
	if leftover != "" {
		delete(localFiles, leftover)
	}
	return errors.Join(collageErrors...)
}

// portraitFeature is what pairing compares: the image's mean Oklab color and
// its aspect ratio.
type portraitFeature struct {
	name   string
	color  oklab
	aspect float64
}

// portraitFeatures measures each portrait one decode at a time and keeps the
// decoded pictures, within the working-set limit, for the collages rendered
// next, so a paired portrait is not decoded twice. A portrait that cannot be
// measured within the working-set budget gets neutral features so it still
// pairs and its error surfaces when the collage is rendered.
func (p *catalogPass) portraitFeatures(ctx context.Context, names []string) []portraitFeature {
	features := make([]portraitFeature, 0, len(names))
	for _, name := range names {
		feature := portraitFeature{name: name, color: oklab{l: 0.5}, aspect: 2.0 / 3.0}
		if len(names) < 2 {
			// A lone portrait has nothing to pair with, so it is not decoded.
			features = append(features, feature)
			continue
		}
		path := filepath.Join(p.artworkDir, name)
		inputs, err := inspectCollageInputs(ctx, path)
		if err == nil {
			err = validateWorkingSet(inputs, p.cfg)
		}
		var img *image.RGBA
		if err == nil {
			// The estimate bounds this decode and the picture it leaves.
			p.releaseDecoded(estimatedWorkingBytes(inputs, p.cfg))
			img, err = loadAndRotateImageWithConfig(ctx, path, p.cfg, p.logger)
		}
		if err == nil {
			feature.color = measurePalette([]*image.RGBA{img}).mean
			feature.aspect = float64(img.Bounds().Dx()) / float64(img.Bounds().Dy())
			p.keepDecoded(path, img)
		} else {
			p.logger.Debug("portrait colors unavailable for pairing", "file", name, "error", err)
		}
		features = append(features, feature)
	}
	return features
}

// pairCost is low for portraits that look good side by side: similar overall
// color, similar lightness, and similar shape for equal-height openings.
func pairCost(first, second portraitFeature) float64 {
	return first.color.distance(second.color) + 0.5*math.Abs(first.color.l-second.color.l) +
		0.5*math.Abs(math.Log(first.aspect/second.aspect))
}

// exactPairingLimit is the most portraits paired by exhaustive search. Its
// table has one entry per subset, so the limit keeps it to a few megabytes.
const exactPairingLimit = 18

// pairPortraits joins portraits so the total cost of all pairs is lowest.
// Taking the best pair first can strand two portraits that suit nothing but
// each other's partners, so up to exactPairingLimit portraits are paired by
// exhaustive search. A larger backlog starts from the best pairs and trades
// partners while a trade lowers the total, which is close but not guaranteed
// lowest. Pairs are ordered by name, so pairing is deterministic. With an odd
// count, the portrait whose absence costs least is returned as the leftover.
func pairPortraits(features []portraitFeature) ([][2]string, string) {
	cost := func(first, second int) float64 { return pairCost(features[first], features[second]) }
	var pairs [][2]int
	leftover := -1
	if len(features) <= exactPairingLimit {
		pairs, leftover = exactPairs(len(features), cost)
	} else {
		pairs, leftover = greedyPairs(len(features), cost)
		for improvePairs(pairs, &leftover, cost) {
			continue
		}
	}
	for index, pair := range pairs {
		pairs[index] = [2]int{min(pair[0], pair[1]), max(pair[0], pair[1])}
	}
	slices.SortFunc(pairs, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	named := make([][2]string, 0, len(pairs))
	for _, pair := range pairs {
		named = append(named, [2]string{features[pair[0]].name, features[pair[1]].name})
	}
	if leftover < 0 {
		return named, ""
	}
	return named, features[leftover].name
}

// exactPairs returns the pairing with the lowest total cost and the unpaired
// index, or -1 when the count is even. It fills a table of the best total for
// every subset: a subset's lowest member must pair with some other member,
// and the rest is a smaller subset already solved. An odd count adds a
// stand-in partner that costs nothing, and whoever takes it is the leftover.
func exactPairs(count int, cost func(first, second int) float64) ([][2]int, int) {
	nodes := count + count%2
	price := func(first, second int) float64 {
		if second >= count {
			return 0
		}
		return cost(first, second)
	}
	best := make([]float64, 1<<nodes)
	partner := make([]uint8, 1<<nodes)
	for subset := 1; subset < len(best); subset++ {
		if bits.OnesCount(uint(subset))%2 != 0 {
			continue
		}
		lowest := bits.TrailingZeros(uint(subset))
		rest := subset &^ (1 << lowest)
		best[subset] = math.Inf(1)
		for other := lowest + 1; other < nodes; other++ {
			if rest&(1<<other) == 0 {
				continue
			}
			if total := price(lowest, other) + best[rest&^(1<<other)]; total < best[subset] {
				best[subset], partner[subset] = total, uint8(other) //nolint:gosec // other is below exactPairingLimit
			}
		}
	}
	var pairs [][2]int
	leftover := -1
	for subset := len(best) - 1; subset > 0; {
		lowest := bits.TrailingZeros(uint(subset))
		other := int(partner[subset])
		if other >= count {
			leftover = lowest
		} else {
			pairs = append(pairs, [2]int{lowest, other})
		}
		subset &^= 1<<lowest | 1<<other
	}
	return pairs, leftover
}

// greedyPairs joins the cheapest pair first, breaking ties by index, and
// returns the pairs with the unpaired index, or -1 when the count is even.
func greedyPairs(count int, cost func(first, second int) float64) ([][2]int, int) {
	type candidate struct {
		cost        float64
		first, next int
	}
	var candidates []candidate
	for i := range count {
		for j := i + 1; j < count; j++ {
			candidates = append(candidates, candidate{cost: cost(i, j), first: i, next: j})
		}
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.cost, b.cost), cmp.Compare(a.first, b.first), cmp.Compare(a.next, b.next))
	})
	paired := make([]bool, count)
	var pairs [][2]int
	for _, option := range candidates {
		if paired[option.first] || paired[option.next] {
			continue
		}
		paired[option.first], paired[option.next] = true, true
		pairs = append(pairs, [2]int{option.first, option.next})
	}
	return pairs, slices.Index(paired, false)
}

// improvePairs makes every partner trade that lowers the total cost in one
// sweep and reports whether it made any. Each trade lowers the total by more
// than rounding error, so repeated sweeps end.
func improvePairs(pairs [][2]int, leftover *int, cost func(first, second int) float64) bool {
	const gain = 1e-9
	improved := false
	for first := range pairs {
		for second := first + 1; second < len(pairs); second++ {
			a, b, c, d := pairs[first][0], pairs[first][1], pairs[second][0], pairs[second][1]
			current := cost(a, b) + cost(c, d)
			switch straight, crossed := cost(a, c)+cost(b, d), cost(a, d)+cost(b, c); {
			case straight < current-gain && straight <= crossed:
				pairs[first], pairs[second], improved = [2]int{a, c}, [2]int{b, d}, true
			case crossed < current-gain:
				pairs[first], pairs[second], improved = [2]int{a, d}, [2]int{b, c}, true
			}
		}
		if *leftover < 0 {
			continue
		}
		a, b := pairs[first][0], pairs[first][1]
		current := cost(a, b)
		switch keepFirst, keepSecond := cost(a, *leftover), cost(b, *leftover); {
		case keepFirst < current-gain && keepFirst <= keepSecond:
			pairs[first], *leftover, improved = [2]int{a, *leftover}, b, true
		case keepSecond < current-gain:
			pairs[first], *leftover, improved = [2]int{b, *leftover}, a, true
		}
	}
	return improved
}
