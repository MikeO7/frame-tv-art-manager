package optimize

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

const stagePattern = ".frame-tv-transform-*"

const (
	DerivativeOptimized DerivativeKind = "optimized"
	DerivativeCollage   DerivativeKind = "collage"
)

// DerivativeKind identifies the transformation that produced an artwork item.
type DerivativeKind string

// StageInput identifies one immutable artwork file to copy into an isolated
// transformation workspace. Digest is the authoritative SHA-256 of its bytes.
type StageInput struct {
	Name         string
	Key          string
	Path         string
	Digest       [sha256.Size]byte
	Width        int
	Height       int
	SourceKeys   []string
	TransformKey string
	Derivative   DerivativeKind
}

// StageRequest describes a complete isolated optimization pass.
type StageRequest struct {
	Inputs []StageInput
	Config Config
	Logger *slog.Logger
}

// Rename records one namespace effect produced inside a Stage.
type Rename struct {
	OldName string
	NewName string
}

// Stage owns an isolated workspace containing the complete transformed
// collection. Call Close after its contents have been consumed.
type Stage struct {
	Directory   string
	Optimized   int
	Renames     []Rename
	Derivatives []Derivative

	closeOnce sync.Once
	closeErr  error
}

// Derivative records the stable lineage of one staged output.
type Derivative struct {
	Name         string
	Inputs       []string
	TransformKey string
	Kind         DerivativeKind
}

// Close removes the complete transformation workspace. It is idempotent.
func (s *Stage) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.Directory != "" {
			s.closeErr = os.RemoveAll(s.Directory)
		}
	})
	return s.closeErr
}

// StageCatalog copies an immutable collection snapshot into a private
// workspace and runs all optimization effects there. Source paths are only
// opened for reading and are never passed to the transformation pipeline.
func StageCatalog(ctx context.Context, request StageRequest) (*Stage, error) {
	if err := validateStageRequest(ctx, request.Inputs); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", stagePattern)
	if err != nil {
		return nil, fmt.Errorf("create optimization stage: %w", err)
	}
	stage := &Stage{Directory: directory}
	cleanupFailure := func(cause error) (*Stage, error) {
		return nil, errors.Join(cause, stage.Close())
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return cleanupFailure(fmt.Errorf("secure optimization stage: %w", err))
	}

	metadata := make(map[string]StageInput, len(request.Inputs))
	for _, input := range request.Inputs {
		if err := copyStageInput(ctx, directory, input); err != nil {
			return cleanupFailure(err)
		}
		metadata[input.Name] = input
	}

	logger := request.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	pass := &catalogPass{artworkDir: directory, cfg: request.Config, logger: logger, inputs: metadata}
	if err := pass.run(ctx); err != nil {
		return cleanupFailure(fmt.Errorf("optimize staged collection: %w", err))
	}
	stage.Optimized, stage.Renames = pass.optimized, pass.renames
	stage.Derivatives = derivativesFromRenames(stage.Renames, TransformKey(request.Config), metadata)
	return stage, nil
}

func derivativesFromRenames(
	renames []Rename,
	transformKey string,
	metadata map[string]StageInput,
) []Derivative {
	grouped := make(map[string][]string)
	for _, rename := range renames {
		if rename.NewName != "" {
			grouped[rename.NewName] = append(grouped[rename.NewName], rename.OldName)
		}
	}
	result := make([]Derivative, 0, len(grouped))
	for _, name := range slices.Sorted(maps.Keys(grouped)) {
		inputs := grouped[name]
		slices.Sort(inputs)
		kind := DerivativeOptimized
		if len(inputs) > 1 || metadata[inputs[0]].Derivative == DerivativeCollage {
			kind = DerivativeCollage
		}
		result = append(result, Derivative{Name: name, Inputs: inputs, TransformKey: transformKey, Kind: kind})
	}
	return result
}

func validateStageRequest(ctx context.Context, inputs []StageInput) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("stage collection before work: %w", err)
	}
	names := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if input.Name == "" || input.Name == "." || filepath.Base(input.Name) != input.Name {
			return fmt.Errorf("stage input name %q must be a plain filename", input.Name)
		}
		if input.Path == "" {
			return fmt.Errorf("stage input %q has an empty path", input.Name)
		}
		if _, exists := names[input.Name]; exists {
			return fmt.Errorf("stage input name %q is duplicated", input.Name)
		}
		names[input.Name] = struct{}{}
	}
	return nil
}
