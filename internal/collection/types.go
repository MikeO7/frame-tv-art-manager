package collection

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
)

// ErrInvalidImport classifies bytes rejected before any Collection mutation.
var ErrInvalidImport = errors.New("invalid artwork import")

const (
	FileTypeJPEG FileType = "jpg"
	FileTypePNG  FileType = "png"

	OriginOperatorUpload OriginClass = "operator-upload"
	OriginOperator       OriginClass = "operator"
	OriginSource         OriginClass = "source"
	OriginDerived        OriginClass = "derived"

	DerivativeOptimized DerivativeKind = "optimized"
	DerivativeCollage   DerivativeKind = "collage"

	ChangeAdded     ChangeKind = "added"
	ChangeAdopted   ChangeKind = "adopted"
	ChangeDuplicate ChangeKind = "duplicate"
	ChangeMissing   ChangeKind = "missing"
)

// Store is the complete artwork-collection boundary. Implementations serialize
// mutations and return only snapshots verified from committed bytes.
type Store interface {
	Prepare(context.Context, PrepareRequest) (Snapshot, error)
	Import(context.Context, ImportRequest) (Snapshot, error)
	Apply(context.Context, ApplyRequest) (Snapshot, error)
}

type Config struct {
	Root           string
	MaxItems       int
	MaxImportBytes int64
	MaxPixels      int64
}

type ImportRequest struct {
	Reader   io.Reader
	Hint     string
	MaxBytes int64
	Policy   Policy
	Origin   Origin
	DryRun   bool
}

// PrepareRequest describes one recovery and inventory cycle.
type PrepareRequest struct {
	// Origins carries exact filename-to-origin associations across a staged
	// namespace transformation. Unlisted files retain their committed origin or
	// are conservatively adopted as operator-owned.
	Origins map[string]Origin
	DryRun  bool
}

// ApplyRequest atomically replaces the committed collection view with the
// validated artwork found in Directory. Directory is treated as untrusted,
// immutable staging input and is never published directly.
type ApplyRequest struct {
	Directory string
	Origins   map[string]Origin
	Metadata  map[string]ItemMetadata
	DryRun    bool
}

// ItemMetadata carries stable logical identity across a staged namespace or
// byte transformation. The staged bytes remain authoritative for media facts.
type ItemMetadata struct {
	Key          string
	Origin       Origin
	SourceKeys   []string
	TransformKey string
	Derivative   DerivativeKind
}

// Policy applies request-specific limits. Zero values retain store limits.
type Policy struct {
	MaxPixels int64
}

type Snapshot struct {
	Generation string
	Items      []Item
	Changes    []Change
	Warnings   []string
	DryRun     bool
}

type Item struct {
	Name   string
	Key    string
	Path   string
	Digest [sha256.Size]byte
	Type   FileType
	Size   int64
	Width  int
	Height int
	Origin Origin
	// SourceKeys retains every stable Source Origin represented by this artwork,
	// including sources combined into a derived collage.
	SourceKeys   []string
	TransformKey string
	Derivative   DerivativeKind
}

type FileType string

type OriginClass string

type DerivativeKind string

type Origin struct {
	Key   string
	Class OriginClass
}

type ChangeKind string

type Change struct {
	Kind ChangeKind
	Name string
}
