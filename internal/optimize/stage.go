package optimize

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const stagePattern = ".frame-tv-transform-*"

const derivativeKindOptimized = "optimized"

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
	Derivative   string
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
	Kind         string
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

	files := make(map[string]struct{}, len(request.Inputs))
	for _, input := range request.Inputs {
		if err := copyStageInput(ctx, directory, input); err != nil {
			return cleanupFailure(err)
		}
		files[input.Name] = struct{}{}
	}

	catalog := &stageCatalog{files: files}
	renames := &renameCollector{}
	logger := request.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metadata := make(map[string]StageInput, len(request.Inputs))
	for _, input := range request.Inputs {
		metadata[input.Name] = input
	}
	stage.Optimized, err = optimizeCatalog(ctx, directory, catalog, request.Config, renames.observe, logger, metadata)
	if err != nil {
		return cleanupFailure(fmt.Errorf("optimize staged collection: %w", err))
	}
	stage.Renames = renames.snapshot()
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
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Derivative, 0, len(names))
	for _, name := range names {
		inputs := grouped[name]
		sort.Strings(inputs)
		kind := derivativeKindOptimized
		if len(inputs) > 1 || metadata[inputs[0]].Derivative == portraitModeCollage {
			kind = portraitModeCollage
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

type stageCatalog struct {
	mu    sync.Mutex
	files map[string]struct{}
}

func (c *stageCatalog) SupportedFiles() (map[string]struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	files := make(map[string]struct{}, len(c.files))
	for name := range c.files {
		files[name] = struct{}{}
	}
	return files, nil
}

func (c *stageCatalog) NoteFileRename(oldName, newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.files, oldName)
	if newName != "" {
		c.files[newName] = struct{}{}
	}
}

type renameCollector struct {
	mu     sync.Mutex
	events []Rename
}

func (c *renameCollector) observe(oldName, newName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, Rename{OldName: oldName, NewName: newName})
	return nil
}

func (c *renameCollector) snapshot() []Rename {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Rename(nil), c.events...)
}
