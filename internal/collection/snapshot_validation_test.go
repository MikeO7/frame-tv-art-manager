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
			Name: "first.png", Key: "first.png", Digest: firstDigest, Type: FileTypePNG, Size: 5, Width: 1, Height: 2,
			Origin: Origin{Key: "operator:first.png", Class: OriginOperator},
		},
		{
			Name: "second.jpg", Key: "second.jpg", Digest: secondDigest, Type: FileTypeJPEG, Size: 6, Width: 2, Height: 3,
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

func TestValidateSnapshotAllowsDerivativeToRetainOriginalUploadKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := sha256.Sum256([]byte("optimized bytes"))
	snapshot := buildSnapshot(root, []Item{{
		Name: "optimized.jpg", Key: "upload.jpg", Digest: digest, Type: FileTypeJPEG,
		Size: 1, Width: 3840, Height: 2160,
		Origin:     Origin{Key: "upload:" + strings.Repeat("1", sha256.Size*2), Class: OriginOperatorUpload},
		Derivative: DerivativeOptimized, TransformKey: strings.Repeat("2", sha256.Size*2),
	}}, nil, false)
	if err := ValidateSnapshot(root, snapshot); err != nil {
		t.Fatalf("ValidateSnapshot() error = %v", err)
	}
}

func TestValidateSnapshotDerivativeMetadataBranches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := sha256.Sum256([]byte("bytes"))
	transform := strings.Repeat("a", sha256.Size*2)
	tests := []struct {
		name   string
		mutate func(*Item)
		want   string
	}{
		{name: "valid optimized", mutate: func(item *Item) { item.Derivative = DerivativeOptimized; item.TransformKey = transform }},
		{name: "valid collage", mutate: func(item *Item) {
			item.Origin = Origin{Key: "derived:collage.jpg", Class: OriginDerived}
			item.Derivative = DerivativeCollage
			item.TransformKey = transform
		}},
		{name: "unsorted sources", mutate: func(item *Item) { item.SourceKeys = []string{"source:z", "source:a"} }, want: "not sorted"},
		{name: "duplicate source", mutate: func(item *Item) { item.SourceKeys = []string{"source:a", "source:a"} }, want: "duplicated"},
		{name: "invalid source", mutate: func(item *Item) { item.SourceKeys = []string{"bad"} }, want: "invalid"},
		{name: "source origin absent", mutate: func(item *Item) {
			item.Origin = Origin{Key: "source:own", Class: OriginSource}
			item.SourceKeys = []string{"source:other"}
		}, want: "absent"},
		{name: "transform without derivative", mutate: func(item *Item) { item.TransformKey = transform }, want: "untransformed"},
		{name: "derived without derivative", mutate: func(item *Item) {
			item.Origin = Origin{Key: "derived:item", Class: OriginDerived}
		}, want: "no derivative"},
		{name: "invalid transform", mutate: func(item *Item) {
			item.Derivative = DerivativeOptimized
			item.TransformKey = "bad"
		}, want: "invalid transform"},
		{name: "collage origin mismatch", mutate: func(item *Item) {
			item.Derivative = DerivativeCollage
			item.TransformKey = transform
		}, want: "disagree"},
		{name: "derived kind mismatch", mutate: func(item *Item) {
			item.Origin = Origin{Key: "derived:item", Class: OriginDerived}
			item.Derivative = DerivativeOptimized
			item.TransformKey = transform
		}, want: "disagree"},
		{name: "unknown derivative", mutate: func(item *Item) { item.Derivative = "mystery" }, want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := Item{
				Name: "item.jpg", Key: "item.jpg", Digest: digest, Type: FileTypeJPEG,
				Size: 5, Width: 1, Height: 1,
				Origin: Origin{Key: "operator:item.jpg", Class: OriginOperator},
			}
			test.mutate(&item)
			err := ValidateSnapshot(root, buildSnapshot(root, []Item{item}, nil, false))
			if test.want == "" && err != nil {
				t.Fatalf("ValidateSnapshot() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("ValidateSnapshot() error = %v, want %q", err, test.want)
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
