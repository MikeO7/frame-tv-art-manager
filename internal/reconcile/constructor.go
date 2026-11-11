package reconcile

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
)

func New(config Config, dependencies Dependencies, options ...option) (Service, error) {
	directory := filepath.Clean(strings.TrimSpace(config.StateDirectory))
	if directory == "." || !filepath.IsAbs(directory) {
		return nil, errors.New("reconciliation state directory must be absolute")
	}
	runtime, err := newRuntimeConfiguration(options)
	if err != nil {
		return nil, err
	}
	legacy, err := newLegacyMappingStore(config.LegacyMappingDirectory)
	if err != nil {
		return nil, err
	}
	if dependencies.Clock == nil {
		dependencies.Clock = wallClock{}
	}
	if dependencies.IDs == nil {
		dependencies.IDs = randomIDs{}
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.Default()
	}
	return &service{
		store: newStateStore(directory), legacy: legacy,
		clock: dependencies.Clock, ids: dependencies.IDs,
		logger: dependencies.Logger.With("component", "reconcile"), mutations: runtime.mutations,
		capacityTTL: runtime.capacityTTL,
	}, nil
}
