package sync

import (
	"log/slog"
	"sync"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// MappingStore loads and caches per-TV filename→content_id mappings.
type MappingStore struct {
	tokenDir string
	tvIPs    []string
	logger   *slog.Logger
	mappings map[string]*Mapping
	mu       sync.Mutex
}

// NewMappingStore creates a mapping store for the configured TVs.
func NewMappingStore(cfg *config.Config, logger *slog.Logger) *MappingStore {
	return &MappingStore{
		tokenDir: cfg.TokenDir,
		tvIPs:    cfg.TVIPs,
		logger:   logger,
		mappings: make(map[string]*Mapping),
	}
}

// Get returns a cached or newly loaded mapping for a TV.
func (s *MappingStore) Get(ip string) (*Mapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m, ok := s.mappings[ip]; ok {
		return m, nil
	}

	m, err := LoadMapping(s.tokenDir, ip)
	if err != nil {
		return nil, err
	}

	s.mappings[ip] = m
	return m, nil
}

// RenameAll migrates a filename across all configured TV mappings.
func (s *MappingStore) RenameAll(oldName, newName string) {
	for _, ip := range s.tvIPs {
		m, err := s.Get(ip)
		if err != nil {
			continue
		}
		if m.Rename(oldName, newName) {
			if err := m.Save(); err != nil {
				s.logger.Warn("failed to save migrated mapping", "tv", ip, "error", err)
			}
		}
	}
}
