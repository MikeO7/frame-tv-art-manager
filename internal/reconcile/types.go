// Package reconcile owns recoverable convergence between one committed artwork
// collection and one Samsung TV.
package reconcile

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const (
	stateVersion = 1
	defaultMatte = "none"
)

var (
	ErrRecoveryRequired   = errors.New("reconciliation recovery required")
	ErrUnsupportedIntent  = errors.New("reconciliation intent is not supported by the Samsung adapter")
	ErrPersistenceUnknown = errors.New("reconciliation persistence outcome unknown")
)

// Service is the complete reconciliation boundary for one TV. Implementations
// serialize calls, and Run always resolves outstanding recovery work first.
type Service interface {
	Run(context.Context, Request) (Result, error)
}

type Config struct {
	StateDirectory         string
	LegacyMappingDirectory string
	Policy                 Policy
}

type Dependencies struct {
	Clock  Clock
	IDs    IDSource
	Logger *slog.Logger
}

type Clock interface {
	Now() time.Time
}

type IDSource interface {
	NewID() (string, error)
}

type Request struct {
	CycleID  string
	TV       samsung.Adapter
	Snapshot collection.Snapshot
	Policy   Policy
	DryRun   bool
}

type Status uint8

const (
	StatusComplete Status = iota
	StatusKnownSkip
	StatusIncompleteDryRun
	StatusUnsupported
	StatusRecoveryRequired
	StatusPersistenceUnknown
	StatusNotApplied
)

type Result struct {
	Status          Status
	Plan            Plan
	State           State
	Observation     samsung.Observation
	Applied         int
	AppliedCommands []CommandKind
}

// Policy is immutable input to a plan. Zero values preserve operator state.
type Policy struct {
	RemoveUnknown  bool
	AllowEmpty     bool
	Select         bool
	DefaultMatte   string
	MatteOverrides MatteOverrides
	Power          PowerPolicy
	Slideshow      SlideshowPolicy
	Brightness     SettingPolicy
}

type PolicyMode uint8

const (
	PolicyPreserve PolicyMode = iota
	PolicyDisable
	PolicySet
)

type PowerPolicy uint8

const (
	PowerPreserve PowerPolicy = iota
	PowerOff
	// PowerOn wakes a TV only after it has been positively observed as off.
	// Unknown power state never authorizes a wake attempt.
	PowerOn
	// WakeWhenKnownOff names the fail-closed behavior implemented by PowerOn.
	WakeWhenKnownOff = PowerOn
)

type SettingPolicy struct {
	Mode  PolicyMode
	Value int
}

// SlideshowPolicy preserves both the interval and Samsung playback kind.
type SlideshowPolicy struct {
	Mode    PolicyMode
	Setting samsung.SlideshowSetting
}

type Phase uint8

const (
	PhasePrepared Phase = iota + 1
	PhaseOutcomeUnknown
	PhaseApplied
)

type CommandKind string

const (
	CommandUpload        CommandKind = "upload"
	CommandDeleteOwned   CommandKind = "delete-owned"
	CommandDeleteUnknown CommandKind = "delete-unknown"
	CommandSelect        CommandKind = "select"
	CommandSlideshow     CommandKind = "slideshow"
	CommandBrightness    CommandKind = "brightness"
	CommandPowerOff      CommandKind = "power-off"
	CommandWake          CommandKind = "wake"
)

type CommandIntent struct {
	Kind                  CommandKind               `json:"kind"`
	Digest                string                    `json:"digest,omitempty"`
	ContentID             string                    `json:"content_id,omitempty"`
	Name                  string                    `json:"name,omitempty"`
	Path                  string                    `json:"path,omitempty"`
	FileType              collection.FileType       `json:"file_type,omitempty"`
	Size                  int64                     `json:"size,omitempty"`
	Matte                 string                    `json:"matte,omitempty"`
	PreviousSlideshow     *samsung.SlideshowSetting `json:"previous_slideshow,omitempty"`
	DesiredSlideshow      *samsung.SlideshowSetting `json:"desired_slideshow,omitempty"`
	PreviousValue         *int                      `json:"previous_value,omitempty"`
	DesiredValue          *int                      `json:"desired_value,omitempty"`
	RemoveUnknownApproved bool                      `json:"remove_unknown_approved,omitempty"`
}

type Plan struct {
	CollectionGeneration string
	PolicyFingerprint    [sha256.Size]byte
	InventoryFingerprint [sha256.Size]byte
	Commands             []CommandIntent
	PruneBindings        []string
}

type TVIdentity struct {
	Address         string `json:"address"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
}

type Binding struct {
	Digest               string    `json:"digest"`
	ContentID            string    `json:"content_id"`
	Name                 string    `json:"name"`
	ArtworkBytes         int64     `json:"artwork_bytes,omitempty"`
	CollectionGeneration string    `json:"collection_generation"`
	ConfirmedAt          time.Time `json:"confirmed_at"`
}

type Tombstone struct {
	Digest     string    `json:"digest,omitempty"`
	ContentID  string    `json:"content_id"`
	RecordedAt time.Time `json:"recorded_at"`
}

type CapacityEvidence struct {
	Known      bool      `json:"known"`
	Maximum    int       `json:"maximum,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Probe      bool      `json:"probe,omitempty"`
}

type InventoryFingerprint struct {
	Digest [sha256.Size]byte `json:"digest"`
}

type ReceiptSummary struct {
	CommandID   string          `json:"command_id"`
	Outcome     samsung.Outcome `json:"outcome"`
	ContentID   string          `json:"content_id,omitempty"`
	CompletedAt time.Time       `json:"completed_at"`
}

type Pending struct {
	OperationID       string               `json:"operation_id"`
	CycleID           string               `json:"cycle_id"`
	CollectionGen     string               `json:"collection_generation"`
	PolicyFingerprint [sha256.Size]byte    `json:"policy_fingerprint"`
	InventoryBefore   InventoryFingerprint `json:"inventory_before"`
	Command           CommandIntent        `json:"command"`
	Phase             Phase                `json:"phase"`
	Receipt           *ReceiptSummary      `json:"receipt,omitempty"`
}

type State struct {
	Version                int                  `json:"version"`
	TV                     TVIdentity           `json:"tv"`
	Revision               uint64               `json:"revision"`
	Bindings               map[string]Binding   `json:"bindings"`
	Tombstones             map[string]Tombstone `json:"tombstones"`
	Capacity               CapacityEvidence     `json:"capacity"`
	LegacyMigrationPending bool                 `json:"legacy_migration_pending,omitempty"`
	Pending                *Pending             `json:"pending,omitempty"`
	LastCompleteCycle      string               `json:"last_complete_cycle_id,omitempty"`
	LastCollectionGen      string               `json:"last_collection_generation,omitempty"`
	Checksum               string               `json:"checksum"`
}
