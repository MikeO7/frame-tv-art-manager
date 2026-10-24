package optimize

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func TestAvailableContentNameExtendsDigestUntilFree(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	digest := sha256.Sum256([]byte("name"))
	shortest := artwork.BuildContentName("label", digest, extJPG, 8)
	if err := os.WriteFile(filepath.Join(directory, shortest), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := contentNameRequest{directory: directory, label: "label", digest: digest, extension: extJPG}
	if got, err := availableContentName(context.Background(), request); err != nil || got != artwork.BuildContentName("label", digest, extJPG, 10) {
		t.Fatalf("availableContentName(occupied shortest) = %q, %v; want the next digest length", got, err)
	}
	request.currentName = shortest
	if got, err := availableContentName(context.Background(), request); err != nil || got != shortest {
		t.Fatalf("availableContentName(current) = %q, %v; want the file's own name", got, err)
	}

	for digestBytes := 8; digestBytes <= sha256.Size; digestBytes += 2 {
		name := artwork.BuildContentName("occupied", digest, extJPG, digestBytes)
		if err := os.WriteFile(filepath.Join(directory, name), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notDirectory := filepath.Join(directory, "not-directory")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, test := range map[string]struct {
		ctx     context.Context
		request contentNameRequest
	}{
		"every name occupied": {context.Background(), contentNameRequest{directory: directory, label: "occupied", digest: digest, extension: extJPG}},
		"non-directory":       {context.Background(), contentNameRequest{directory: notDirectory, label: "label", digest: digest, extension: extJPG}},
		"canceled":            {canceled, contentNameRequest{directory: directory, label: "label", digest: digest, extension: extJPG}},
	} {
		if got, err := availableContentName(test.ctx, test.request); err == nil {
			t.Errorf("availableContentName(%s) = %q, want error", name, got)
		}
	}
}

func TestFileDigestHashesRegularFilesAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "art.jpg")
	if err := os.WriteFile(path, []byte("artwork bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := fileDigest(context.Background(), path); err != nil || got != sha256.Sum256([]byte("artwork bytes")) {
		t.Fatalf("fileDigest() = %x, %v", got, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fileDigest(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("fileDigest(canceled) error = %v", err)
	}
	for _, invalid := range []string{filepath.Join(directory, "missing"), directory} {
		if _, err := fileDigest(context.Background(), invalid); err == nil {
			t.Errorf("fileDigest(%s) error = nil", invalid)
		}
	}
}

func TestValidateImageRequiresCompleteDecodableImage(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.jpg")
	writeTestImage(t, valid, 8, 6)
	invalid := filepath.Join(directory, "invalid.jpg")
	if err := os.WriteFile(invalid, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		path    string
		wantErr bool
	}{
		{name: "valid", ctx: context.Background(), path: valid},
		{name: "not an image", ctx: context.Background(), path: invalid, wantErr: true},
		{name: "missing", ctx: context.Background(), path: filepath.Join(directory, "missing.jpg"), wantErr: true},
		{name: "canceled", ctx: canceled, path: valid, wantErr: true},
	}
	for _, test := range tests {
		if err := ValidateImage(test.ctx, test.path); (err != nil) != test.wantErr {
			t.Errorf("ValidateImage(%s) error = %v, wantErr %t", test.name, err, test.wantErr)
		}
	}
}
