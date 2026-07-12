package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

type preparedCollection struct {
	files      map[string]struct{}
	downloaded int
	optimized  int
	warnings   []string
}

type localCollection struct {
	cfg     *config.Config
	logger  *slog.Logger
	health  *health.Status
	loader  sources.SourceLoader
	catalog *sources.ArtworkCatalog
}

var (
	prepareOptimizeCatalog       = optimize.OptimizeCatalog
	prepareCatalogSupportedFiles = func(catalog *sources.ArtworkCatalog) (map[string]struct{}, error) {
		return catalog.SupportedFiles()
	}
)

func newLocalCollection(cfg *config.Config, logger *slog.Logger, healthStatus *health.Status) *localCollection {
	catalog := sources.NewArtworkCatalog(cfg.ArtworkDir, logger)
	return &localCollection{
		cfg: cfg, logger: logger, health: healthStatus,
		loader: sources.NewLoader(cfg, logger, catalog), catalog: catalog,
	}
}

func (c *localCollection) prepare(ctx context.Context) (preparedCollection, error) {
	var result preparedCollection
	if c.health != nil {
		c.health.SetStage("downloading sources")
	}
	downloaded, sourceErr := c.loader.Sync(ctx)
	result.downloaded = downloaded
	if sourceErr != nil {
		c.logger.Warn("source download error", "error", sourceErr)
		result.warnings = append(result.warnings, fmt.Sprintf("Source download issue: %v", sourceErr))
	}

	if c.health != nil {
		c.health.SetStage("optimizing artwork")
	}
	optimized, err := prepareOptimizeCatalog(
		ctx, c.cfg.ArtworkDir, c.catalog, c.cfg.OptimizeOptions(), c.observeRename, c.logger,
	)
	if err != nil {
		return result, err
	}
	result.optimized = optimized
	if optimized > 0 {
		c.catalog.InvalidateCache()
	}
	result.files, err = prepareCatalogSupportedFiles(c.catalog)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (c *localCollection) observeRename(oldName, newName string) error {
	var renameErrors []error
	for _, ip := range c.cfg.TVIPs {
		mapping, err := LoadMapping(c.cfg.TokenDir, ip)
		if err != nil {
			renameErrors = append(renameErrors, fmt.Errorf("load mapping for TV %s: %w", ip, err))
			continue
		}
		if _, err := mapping.Rename(oldName, newName); err != nil {
			renameErrors = append(renameErrors, fmt.Errorf("rename mapping for TV %s: %w", ip, err))
		}
	}
	return errors.Join(renameErrors...)
}
