package health

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestAtomicWriteArtwork_WriteErrorOnSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upload.jpg")

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Skipf("Getrlimit unsupported: %v", err)
	}

	if old.Cur == 0 {
		t.Skip("file size limit already zero; cannot drive write error branch deterministically")
	}

	limited := old
	limited.Cur = 1
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Skipf("setrlimit failed: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
			t.Fatalf("restore RLIMIT_FSIZE: %v", err)
		}
	})

	if err := atomicWriteArtwork(path, []byte{0xAB, 0xCD, 0xEF}); err == nil {
		t.Fatalf("expected write failure due size limit, got nil")
	}
}
