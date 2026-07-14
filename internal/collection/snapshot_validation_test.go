package collection

import (
	"crypto/sha256"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSnapshot(t *testing.T) {
	root := t.TempDir()
	firstDigest := sha256.Sum256([]byte("first"))
	secondDigest := sha256.Sum256([]byte("second"))
	valid := buildSnapshot(root, []Item{
		{
			Name: "first.png", Digest: firstDigest, Type: FileTypePNG, Size: 5, Width: 1, Height: 2,
			Origin: Origin{Key: "operator:first.png", Class: OriginOperator},
		},
		{
			Name: "second.jpg", Digest: secondDigest, Type: FileTypeJPEG, Size: 6, Width: 2, Height: 3,
			Origin: Origin{Key: "upload:" + stringHex(secondDigest[:]), Class: OriginOperatorUpload},
		},
	}, nil, false)

	tests := []struct {
		name      string
		root      string
		mutate    func(*Snapshot)
		wantError string
	}{
		{name: "valid", root: root},
		{name: "relative root", root: "relative", wantError: "root"},
		{name: "generation missing", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Generation = ""
		}, wantError: "generation"},
		{name: "generation mismatch", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Generation = strings.Repeat("0", sha256.Size*2)
		}, wantError: "generation does not match"},
		{name: "zero digest", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Digest = [sha256.Size]byte{}
		}, wantError: "digest"},
		{name: "zero size", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Size = 0
		}, wantError: "size"},
		{name: "zero width", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Width = 0
		}, wantError: "dimensions"},
		{name: "zero height", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Height = 0
		}, wantError: "dimensions"},
		{name: "unsupported type", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Type = "gif"
		}, wantError: "type"},
		{name: "type and extension mismatch", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Type = FileTypeJPEG
		}, wantError: "type"},
		{name: "empty origin key", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Origin.Key = ""
		}, wantError: "origin"},
		{name: "unknown origin class", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Origin.Class = "source-managed"
		}, wantError: "origin"},
		{name: "operator origin prefix", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Origin.Key = "upload:first"
		}, wantError: "origin"},
		{name: "upload origin digest", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[1].Origin.Key = "upload:" + strings.Repeat("0", sha256.Size*2)
		}, wantError: "origin"},
		{name: "escaped path", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Path = filepath.Join(filepath.Dir(root), "first.png")
		}, wantError: "path"},
		{name: "noncanonical path", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Path = root + string(filepath.Separator) + "nested" + string(filepath.Separator) +
				".." + string(filepath.Separator) + "first.png"
		}, wantError: "path"},
		{name: "control file", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[0].Name = "._first.png"
			snapshot.Items[0].Path = filepath.Join(root, "._first.png")
			snapshot.Items[0].Origin.Key = "operator:._first.png"
		}, wantError: "name"},
		{name: "duplicate name", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[1].Name = "FIRST.PNG"
			snapshot.Items[1].Path = filepath.Join(root, "FIRST.PNG")
			snapshot.Items[1].Type = FileTypePNG
		}, wantError: "repeats name"},
		{name: "duplicate digest", root: root, mutate: func(snapshot *Snapshot) {
			snapshot.Items[1].Digest = snapshot.Items[0].Digest
		}, wantError: "repeats digest"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := valid
			snapshot.Items = cloneItems(valid.Items)
			if tc.mutate != nil {
				tc.mutate(&snapshot)
			}
			err := ValidateSnapshot(tc.root, snapshot)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateSnapshot() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("ValidateSnapshot() error = %v, want containing %q", err, tc.wantError)
			}
		})
	}
}

func TestValidateSnapshotAcceptsEmptyCommittedCollection(t *testing.T) {
	root := t.TempDir()
	if err := ValidateSnapshot(root, buildSnapshot(root, nil, nil, false)); err != nil {
		t.Fatalf("ValidateSnapshot() error = %v", err)
	}
}
