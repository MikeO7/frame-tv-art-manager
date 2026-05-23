package sync

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

// Mapping persists the filename→content_id relationship for a single TV.
type Mapping struct {
	mu   sync.RWMutex
	path string
	data map[string]string // filename → content_id
}

// LoadMapping reads a mapping file from disk.
func LoadMapping(dir, tvIP string) (*Mapping, error) {
	safeIP := strings.ReplaceAll(tvIP, ".", "_")
	path := filepath.Clean(filepath.Join(dir, fmt.Sprintf("tv_%s_mapping.json", safeIP)))

	m := &Mapping{
		path: path,
		data: make(map[string]string),
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil // new mapping, will be created on Save
		}
		return nil, fmt.Errorf("read mapping %s: %w", path, err)
	}

	if err := json.Unmarshal(raw, &m.data); err != nil {
		return nil, fmt.Errorf("parse mapping %s: %w", path, err)
	}

	return m, nil
}

// Save writes the mapping to disk as formatted JSON.
func (m *Mapping) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("create mapping dir: %w", err)
	}

	raw, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mapping: %w", err)
	}

	return os.WriteFile(m.path, raw, 0o600)
}

// Set records a filename→content_id association.
func (m *Mapping) Set(filename string, contentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[filename] = contentID
}

// Delete removes a filename from the mapping.
func (m *Mapping) Delete(filename string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, filename)
}

// DeleteBatch removes multiple filenames from the mapping under a single lock.
func (m *Mapping) DeleteBatch(filenames []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, filename := range filenames {
		delete(m.data, filename)
	}
}

// Rename updates a filename in the mapping while preserving its content_id.
// Returns true if the old filename was found and migrated.
func (m *Mapping) Rename(oldName, newName string) bool {
	return m.renameInternal(oldName, newName)
}

func (m *Mapping) renameInternal(oldName, newName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.data[oldName]; ok {
		delete(m.data, oldName)
		m.data[newName] = id
		return true
	}
	return false
}

// GetContentID returns the content_id for a filename, and whether it exists.
func (m *Mapping) GetContentID(filename string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.data[filename]
	return id, ok
}

// GetFilename returns the filename for a content_id, and whether it exists.
func (m *Mapping) GetFilename(contentID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for f, id := range m.data {
		if id == contentID {
			return f, true
		}
	}
	return "", false
}

// AllContentIDs returns a copy of the full filename→content_id map.
func (m *Mapping) AllContentIDs() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.data))
	for k, v := range m.data {
		out[k] = v
	}
	return out
}

// TrackedFilenames returns the set of filenames that have known content IDs.
func (m *Mapping) TrackedFilenames() map[string]struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]struct{}, len(m.data))
	for k := range m.data {
		out[k] = struct{}{}
	}
	return out
}

// MatteConfig holds per-image matte overrides loaded from a mattes.json file.
type MatteConfig struct {
	overrides    map[string]string
	defaultMatte string
}

// LoadMatteConfig reads a mattes.json file from the artwork directory.
func LoadMatteConfig(artworkDir string) *MatteConfig {
	mc := &MatteConfig{
		overrides: make(map[string]string),
	}

	path := filepath.Join(artworkDir, "mattes.json")
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return mc
	}

	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		return mc
	}

	for k, v := range data {
		if k == "_default" {
			mc.defaultMatte = v
		} else {
			mc.overrides[k] = v
		}
	}

	return mc
}

// GetMatte returns the matte style for a specific filename.
func (mc *MatteConfig) GetMatte(filename, globalMatte string) string {
	if matte, ok := mc.overrides[filename]; ok {
		return matte
	}
	if mc.defaultMatte != "" {
		return mc.defaultMatte
	}
	return globalMatte
}

// String returns a summary of the matte configuration for logging.
func (mc *MatteConfig) String() string {
	if len(mc.overrides) == 0 && mc.defaultMatte == "" {
		return "global (no per-file overrides)"
	}
	return fmt.Sprintf("%d per-file overrides, default=%q", len(mc.overrides), mc.defaultMatte)
}

// Collection orchestrates the local file scanning, worker resizing, renaming, and mapping DB operations.
type Collection struct {
	cfg            *config.Config
	logger         *slog.Logger
	mappings       map[string]*Mapping
	mapMu          sync.Mutex
	lastLocalFiles map[string]struct{}
	lastDirModTime time.Time
}

// NewCollection instantiates a new local collection manager.
func NewCollection(cfg *config.Config, logger *slog.Logger) *Collection {
	return &Collection{
		cfg:      cfg,
		logger:   logger,
		mappings: make(map[string]*Mapping),
	}
}

// GetMapping returns a cached or newly loaded mapping for a TV.
func (c *Collection) GetMapping(ip string) (*Mapping, error) {
	c.mapMu.Lock()
	defer c.mapMu.Unlock()

	if m, ok := c.mappings[ip]; ok {
		return m, nil
	}

	m, err := LoadMapping(c.cfg.TokenDir, ip)
	if err != nil {
		return nil, err
	}

	c.mappings[ip] = m
	return m, nil
}

// ScanAndOptimize performs directory scanning and worker-pool image optimizations.
func (c *Collection) ScanAndOptimize(cycleLog *slog.Logger) (map[string]struct{}, int, error) {
	info, statErr := os.Stat(c.cfg.ArtworkDir)
	if statErr == nil {
		if info.ModTime().Equal(c.lastDirModTime) && c.lastLocalFiles != nil {
			cycleLog.Debug("skipping disk scan — directory ModTime unchanged")
			localFiles := make(map[string]struct{}, len(c.lastLocalFiles))
			for k := range c.lastLocalFiles {
				localFiles[k] = struct{}{}
			}
			return localFiles, 0, nil
		}
		c.lastDirModTime = info.ModTime()
	}

	localFiles, err := ScanArtworkDir(c.cfg.ArtworkDir)
	if err != nil {
		return nil, 0, fmt.Errorf("scan artwork: %w", err)
	}
	c.lastLocalFiles = localFiles

	optimized := c.OptimizeLocalArtwork(localFiles, cycleLog)

	cycleLog.Info("local artwork ready", "total", len(localFiles), "optimized", optimized)
	return localFiles, optimized, nil
}

// OptimizeLocalArtwork drives optimization worker threads.
func (c *Collection) OptimizeLocalArtwork(localFiles map[string]struct{}, cycleLog *slog.Logger) int {
	var optimizedCount int64

	optCfg := optimize.Config{
		Enabled:             c.cfg.OptimizeEnabled,
		MaxWidth:            c.cfg.OptimizeMaxWidth,
		MaxHeight:           c.cfg.OptimizeMaxHeight,
		OptimizeJPEGQuality: c.cfg.OptimizeJPEGQuality,
		MuseumModeEnabled:   c.cfg.MuseumModeEnabled,
		MuseumModeIntensity: c.cfg.MuseumModeIntensity,
	}

	type job struct {
		filename string
	}
	jobs := make(chan job, len(localFiles))
	for filename := range localFiles {
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		jobs <- job{filename: filename}
	}
	close(jobs)

	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 16 {
		numWorkers = 16
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				wasModified, ok := c.HandleSingleOptimization(j.filename, localFiles, optCfg, &mu, cycleLog)
				if ok && wasModified {
					atomic.AddInt64(&optimizedCount, 1)
				}
			}
		}()
	}

	wg.Wait()
	return int(optimizedCount)
}

// HandleSingleOptimization handles resizing/validating a single image.
func (c *Collection) HandleSingleOptimization(filename string, localFiles map[string]struct{}, optCfg optimize.Config, mu *sync.Mutex, log *slog.Logger) (bool, bool) {
	path := filepath.Join(c.cfg.ArtworkDir, filename)

	if !optCfg.Enabled {
		if err := optimize.ValidateImage(path); err != nil {
			log.Warn("skipping corrupt image", "file", filename, "error", err)
			mu.Lock()
			delete(localFiles, filename)
			mu.Unlock()
			return false, false
		}
		return false, true
	}

	newFilename, modified, err := optimize.OptimizeFile(path, optCfg, log)
	if err != nil {
		log.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		mu.Lock()
		delete(localFiles, filename)
		mu.Unlock()
		return false, false
	}

	if modified && newFilename != filename {
		c.UpdateMappings(filename, newFilename)
		mu.Lock()
		delete(localFiles, filename)
		localFiles[newFilename] = struct{}{}
		mu.Unlock()
	}

	return modified, true
}

// EnsureCorrectFilename formats filename with dims and hash on modifications.
func (c *Collection) EnsureCorrectFilename(filename string, newW, newH int, modified bool, localFiles map[string]struct{}, mu *sync.Mutex) {
	currentW, currentH, _ := parseDimensions(filename)
	isOpt := strings.Contains(filename, "_opt.h_")

	if !modified && isOpt && currentW == newW && currentH == newH {
		return
	}

	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)
	var hash string

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
		hash = parts[1]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		hash = parts[len(parts)-1]
		identity = strings.Join(parts[:len(parts)-1], "__")
	} else {
		hash = "local"
	}

	if lastUnderscore := strings.LastIndex(identity, "_"); lastUnderscore != -1 {
		suffix := identity[lastUnderscore+1:]
		if strings.Contains(suffix, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(suffix, "%dx%d", &w, &h); n == 2 {
				identity = identity[:lastUnderscore]
			}
		}
	}
	identity = strings.Split(identity, "_opt")[0]

	newFilename := fmt.Sprintf("%s_%dx%d_opt.h_%s%s", identity, newW, newH, hash, ext)
	if newFilename == filename {
		return
	}

	path := filepath.Join(c.cfg.ArtworkDir, filename)
	newPath := filepath.Join(c.cfg.ArtworkDir, newFilename)

	if err := os.Rename(path, newPath); err == nil {
		c.logger.Info("updated optimized filename", "old", filename, "new", newFilename)
		c.UpdateMappings(filename, newFilename)
		mu.Lock()
		delete(localFiles, filename)
		localFiles[newFilename] = struct{}{}
		mu.Unlock()
	}
}

// UpdateMappings migrates mappings from old name to new name.
func (c *Collection) UpdateMappings(oldName, newName string) {
	for _, ip := range c.cfg.TVIPs {
		m, err := c.GetMapping(ip)
		if err != nil {
			continue
		}
		if m.Rename(oldName, newName) {
			if err := m.Save(); err != nil {
				c.logger.Warn("failed to save migrated mapping", "tv", ip, "error", err)
			}
		}
	}
}

// parseDimensions extracts width and height from a filename like "..._3840x2160_opt.h_...".
func parseDimensions(filename string) (int, int, bool) {
	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		identity = strings.Join(parts[:len(parts)-1], "__")
	}

	parts := strings.Split(identity, "_")
	for _, p := range parts {
		if strings.Contains(p, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(p, "%dx%d", &w, &h); n == 2 {
				return w, h, true
			}
		}
	}
	return 0, 0, false
}

var supportedExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
}

// ScanArtworkDir reads a directory and returns supported image filenames.
func ScanArtworkDir(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artwork directory does not exist: %s", dir)
		}
		return nil, fmt.Errorf("read artwork dir %s: %w", dir, err)
	}

	files := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if supportedExtensions[ext] {
			files[entry.Name()] = struct{}{}
		}
	}

	return files, nil
}

const (
	extPNG = "png"
	extJPG = "jpg"
)

// FileTypeFromExt returns the compatible file type string.
func FileTypeFromExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png":
		return extPNG
	default:
		return extJPG
	}
}
