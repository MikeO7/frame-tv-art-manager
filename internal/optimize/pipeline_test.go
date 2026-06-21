package optimize

import (
	"context"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

type mockCatalog struct {
	files   map[string]struct{}
	renamed map[string]string
}

func (m *mockCatalog) SupportedFiles() (map[string]struct{}, error) {
	return m.files, nil
}

func (m *mockCatalog) NoteFileRename(oldName, newName string) {
	if m.renamed == nil {
		m.renamed = make(map[string]string)
	}
	m.renamed[oldName] = newName
}

func TestRecordDelete(t *testing.T) {
	o := &optContext{
		localFiles: map[string]struct{}{
			"test1.jpg": {},
			"test2.jpg": {},
		},
	}

	o.recordDelete("test1.jpg")

	if _, ok := o.localFiles["test1.jpg"]; ok {
		t.Errorf("expected test1.jpg to be deleted")
	}
	if _, ok := o.localFiles["test2.jpg"]; !ok {
		t.Errorf("expected test2.jpg to remain")
	}
}

func TestRecordRename(t *testing.T) {
	o := &optContext{
		localFiles: map[string]struct{}{
			"old.jpg": {},
		},
	}

	o.recordRename("old.jpg", "new.jpg")

	if _, ok := o.localFiles["old.jpg"]; ok {
		t.Errorf("expected old.jpg to be deleted")
	}
	if _, ok := o.localFiles["new.jpg"]; !ok {
		t.Errorf("expected new.jpg to exist")
	}

	o.recordRename("new.jpg", "")
	if len(o.localFiles) != 0 {
		t.Errorf("expected no files to exist, got %v", o.localFiles)
	}
}

func TestClampWorkers(t *testing.T) {
	tests := []struct {
		in  int
		out int
	}{
		{0, minOptimizeWorkers},
		{minOptimizeWorkers - 1, minOptimizeWorkers},
		{minOptimizeWorkers, minOptimizeWorkers},
		{minOptimizeWorkers + 1, minOptimizeWorkers + 1},
		{maxOptimizeWorkers - 1, maxOptimizeWorkers - 1},
		{maxOptimizeWorkers, maxOptimizeWorkers},
		{maxOptimizeWorkers + 1, maxOptimizeWorkers},
		{100, maxOptimizeWorkers},
	}

	for _, tc := range tests {
		if got := clampWorkers(tc.in); got != tc.out {
			t.Errorf("clampWorkers(%d) = %d; want %d", tc.in, got, tc.out)
		}
	}
}

func TestEnqueueOptimizeJobs(t *testing.T) {
	localFiles := map[string]struct{}{
		"test1.jpg":   {},
		"._test2.jpg": {},
		"test3.png":   {},
	}

	jobsCh := enqueueOptimizeJobs(localFiles)

	// Collect jobs from channel
	jobs := make([]string, 0, 2)
	for job := range jobsCh {
		jobs = append(jobs, job)
	}

	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs))
	}

	// Check AppleDouble file was removed from the map
	if _, ok := localFiles["._test2.jpg"]; ok {
		t.Errorf("expected ._test2.jpg to be removed from localFiles")
	}
	if _, ok := localFiles["test1.jpg"]; !ok {
		t.Errorf("expected test1.jpg to remain in localFiles")
	}
	if _, ok := localFiles["test3.png"]; !ok {
		t.Errorf("expected test3.png to remain in localFiles")
	}
}

func TestRunOptimizeWorkers(t *testing.T) {
	// Simple test to ensure it runs and respects context cancellation
	o := &optContext{
		artworkDir: t.TempDir(),
		cfg:        Config{Enabled: false},
		logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan string, 1)
	jobs <- "test1.jpg"
	close(jobs)

	var count int64
	runOptimizeWorkers(ctx, jobs, o, &count)

	// Since file doesn't exist and cfg.Enabled is false, it should return false, false
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}

	// Test cancellation
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2() // cancel immediately

	jobs2 := make(chan string, 1)
	jobs2 <- "test2.jpg"
	close(jobs2)

	runOptimizeWorkers(ctx2, jobs2, o, &count)
}

func TestOptimizeCatalog(t *testing.T) {
	artworkDir := t.TempDir()
	cat := &mockCatalog{
		files: map[string]struct{}{
			"test1.jpg": {},
		},
	}
	cfg := Config{Enabled: false}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Since test1.jpg doesn't exist, it should skip it and return count 0
	count, err := OptimizeCatalog(context.Background(), artworkDir, cat, cfg, nil, logger)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}

	// Test cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = OptimizeCatalog(ctx, artworkDir, cat, cfg, nil, logger)
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}

	// Test catalog error
	catError := &mockCatalogWithError{}
	_, err = OptimizeCatalog(context.Background(), artworkDir, catError, cfg, nil, logger)
	if err == nil {
		t.Fatalf("expected catalog error")
	}
}

type mockCatalogWithError struct{}

func (m *mockCatalogWithError) SupportedFiles() (map[string]struct{}, error) {
	return nil, context.DeadlineExceeded // arbitrary error
}
func (m *mockCatalogWithError) NoteFileRename(oldName, newName string) {}

func TestIsPortraitFile(t *testing.T) {
	// Create a dummy file that is not a valid image
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.jpg")
	err := os.WriteFile(path, []byte("not an image"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = isPortraitFile(path)
	if err == nil {
		t.Fatalf("expected error for invalid image")
	}

	// Test non-existent file
	_, err = isPortraitFile(filepath.Join(dir, "missing.jpg"))
	if err == nil {
		t.Fatalf("expected error for missing image")
	}
}

func TestLoadAndRotateImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.jpg")
	err := os.WriteFile(path, []byte("not an image"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = loadAndRotateImage(path)
	if err == nil {
		t.Fatalf("expected error for invalid image")
	}

	// Test non-existent file
	_, err = loadAndRotateImage(filepath.Join(dir, "missing.jpg"))
	if err == nil {
		t.Fatalf("expected error for missing image")
	}
}

func TestProcessCollagePair(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{}
	cat := &mockCatalog{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Should fail immediately because files don't exist
	_, err := processCollagePair(dir, "f1.jpg", "f2.jpg", cfg, cat, nil, logger)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestProcessCollages(t *testing.T) {
	dir := t.TempDir()
	cat := &mockCatalog{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var count int64

	localFiles := map[string]struct{}{
		"upload1.jpg": {},
		"upload2.jpg": {},
		"upload3.jpg": {},
	}
	cfg := Config{}

	processCollages(dir, localFiles, cfg, cat, nil, logger, &count)
	// count should be 0 because the files aren't valid images so they are skipped in collectRawPortraits
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

func TestCollectRawPortraits(t *testing.T) {
	dir := t.TempDir()
	localFiles := map[string]struct{}{
		"._upload1.jpg": {},
		"upload2.jpg":   {},
		"other.jpg":     {},
	}

	// Create an invalid image file so isPortraitFile returns an error
	path := filepath.Join(dir, "upload2.jpg")
	err := os.WriteFile(path, []byte("not an image"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	res := collectRawPortraits(dir, localFiles, false)
	if len(res) != 0 {
		t.Errorf("expected 0 portraits, got %d", len(res))
	}
}

func TestHandleSingleOptimization(t *testing.T) {
	dir := t.TempDir()
	o := &optContext{
		artworkDir: dir,
		localFiles: map[string]struct{}{
			"test.jpg": {},
		},
		cfg:     Config{Enabled: false},
		catalog: &mockCatalog{},
		logger:  slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	// Missing file
	mod1, ok1 := handleSingleOptimization("test.jpg", o)
	if mod1 || ok1 {
		t.Errorf("expected false and false, got %v, %v", mod1, ok1)
	}

	// Enabled, but missing file -> error
	o.cfg.Enabled = true
	o.localFiles["test.jpg"] = struct{}{}
	mod2, ok2 := handleSingleOptimization("test.jpg", o)
	if mod2 || ok2 {
		t.Errorf("expected false and false, got %v, %v", mod2, ok2)
	}
}

// Create a valid dummy image file for testing loading/saving without full processing
func createDummyImage(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = jpeg.Encode(f, img, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessCollages_WithValidImages(t *testing.T) {
	dir := t.TempDir()
	cat := &mockCatalog{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var count int64

	// Create two valid upload files which are portrait shaped (width < height)
	// We'll construct actual portrait images to pass isPortraitFile check
	f1 := filepath.Join(dir, "upload1.jpg")
	f2 := filepath.Join(dir, "upload2.jpg")

	// Make portrait images (w < h)
	img := image.NewRGBA(image.Rect(0, 0, 10, 20))

	out1, _ := os.Create(f1)
	_ = jpeg.Encode(out1, img, nil)
	out1.Close()

	out2, _ := os.Create(f2)
	_ = jpeg.Encode(out2, img, nil)
	out2.Close()

	localFiles := map[string]struct{}{
		"upload1.jpg": {},
		"upload2.jpg": {},
	}
	cfg := Config{
		MaxWidth:  100,
		MaxHeight: 100,
	}

	processCollages(dir, localFiles, cfg, cat, nil, logger, &count)

	// count should be incremented for each pair processed, which means +1 since it optimized 1 pair
	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}
}

func TestHandleSingleOptimization_ValidImage(t *testing.T) {
	dir := t.TempDir()
	f1 := "test1.jpg"
	path1 := filepath.Join(dir, f1)

	createDummyImage(t, path1)

	o := &optContext{
		artworkDir: dir,
		localFiles: map[string]struct{}{
			f1: {},
		},
		cfg:     Config{Enabled: false},
		catalog: &mockCatalog{},
		logger:  slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	// Enabled = false, valid image
	mod1, ok1 := handleSingleOptimization(f1, o)
	if mod1 || !ok1 {
		t.Errorf("expected false, true, got %v, %v", mod1, ok1)
	}

	// Delete file
	os.Remove(path1)

	// Create new valid image, Enabled = true
	createDummyImage(t, path1)
	o.cfg = Config{
		Enabled:   true,
		MaxWidth:  10,
		MaxHeight: 10,
	}

	mod2, ok2 := handleSingleOptimization(f1, o)
	// OptimizeFile will process it. Note the original code does renaming via artwork.BuildOptimizedName
	if !mod2 || !ok2 {
		t.Errorf("expected true and true, got %v, %v", mod2, ok2)
	}
}
