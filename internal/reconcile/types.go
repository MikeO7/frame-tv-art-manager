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
	Clock  clock
	IDs    idSource
	Logger *slog.Logger
}

type clock interface {
	Now() time.Time
}

type idSource interface {
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
	StatusStorageFull
)

type Result struct {
	Status          Status
	Plan            Plan
	State           State
	Observation     samsung.Observation
	Applied         int
	AppliedCommands []CommandKind
	Rotation        RotationStatus
	// powerOffAttempted records that this run already sent the scheduled
	// power-off, so a failure is never retried in the same cycle.
	powerOffAttempted bool
}

// RotationStatus summarizes artwork rotation on a TV whose known storage
// capacity is smaller than the Artwork Collection.
type RotationStatus struct {
	// Active reports that rotation is handling the capacity shortfall.
	Active bool
	// Waiting counts desired artwork that is not on the TV after the plan.
	Waiting int
	// NextAt is the earliest time another swap can become due. It is zero when
	// no further swap is pending.
	NextAt time.Time
}

// Policy is immutable input to a plan. Zero values preserve operator state.
type Policy struct {
	RemoveUnknown bool
	AllowEmpty    bool
	// Show has the manager change the picture on screen itself. The zero
	// value leaves what is shown to the TV.
	Show         ShowPolicy
	DefaultMatte string
	// PortraitMatte is the matte the TV uses when it shows artwork in portrait
	// orientation. Empty uses the landscape matte.
	PortraitMatte  string
	MatteOverrides MatteOverrides
	Power          PowerPolicy
	Slideshow      SlideshowPolicy
	Brightness     SettingPolicy
	// Rotation is how long owned artwork stays on a full TV before it may be
	// swapped for artwork that is waiting. Zero disables rotation.
	Rotation time.Duration
	// WakeBoundary ties PowerOn to one scheduled power-on time. The TV is
	// woken at most once per boundary, recorded durably. Zero keeps the
	// unscheduled PowerOn behavior.
	WakeBoundary time.Time
}

// showOrder is the order in which the manager changes the picture on screen.
type showOrder uint8

const (
	// ShowShuffle shows every picture once, in a new random order each round.
	ShowShuffle showOrder = iota
	// ShowSequential shows pictures in name order and wraps around.
	ShowSequential
)

// ShowPolicy changes the picture on screen every Interval. Samsung's own
// slideshow of uploaded art needs an Art Store subscription on current
// firmware, so the manager selects each picture itself.
type ShowPolicy struct {
	Interval time.Duration
	Order    showOrder
}

type policyMode uint8

const (
	PolicyPreserve policyMode = iota
	PolicyDisable
	PolicySet
	// PolicyAdjust changes a slideshow only while the TV already plays one: a
	// zero interval or empty kind keeps the value the TV reports. It never
	// turns a slideshow on or off.
	PolicyAdjust
)

type PowerPolicy uint8

const (
	PowerPreserve PowerPolicy = iota
	PowerOff
	// PowerOn wakes a TV only after it has been positively observed as off.
	// Unknown power state never authorizes a wake attempt.
	PowerOn
)

type SettingPolicy struct {
	Mode  policyMode
	Value int
}

// SlideshowPolicy preserves both the interval and Samsung playback kind.
type SlideshowPolicy struct {
	Mode    policyMode
	Setting samsung.SlideshowSetting
}

type phase uint8

const (
	phasePrepared phase = iota + 1
	phaseOutcomeUnknown
	phaseApplied
)

type CommandKind string

const (
	CommandUpload        CommandKind = "upload"
	CommandDeleteOwned   CommandKind = "delete-owned"
	CommandDeleteUnknown CommandKind = "delete-unknown"
	commandSelect        CommandKind = "select"
	commandSlideshow     CommandKind = "slideshow"
	CommandBrightness    CommandKind = "brightness"
	commandPowerOff      CommandKind = "power-off"
	CommandWake          CommandKind = "wake"
)

type commandIntent struct {
	Kind                  CommandKind               `json:"kind"`
	Digest                string                    `json:"digest,omitempty"`
	ContentID             string                    `json:"content_id,omitempty"`
	Name                  string                    `json:"name,omitempty"`
	Path                  string                    `json:"path,omitempty"`
	FileType              collection.FileType       `json:"file_type,omitempty"`
	Size                  int64                     `json:"size,omitempty"`
	Matte                 string                    `json:"matte,omitempty"`
	PortraitMatte         string                    `json:"portrait_matte,omitempty"`
	PreviousSlideshow     *samsung.SlideshowSetting `json:"previous_slideshow,omitempty"`
	DesiredSlideshow      *samsung.SlideshowSetting `json:"desired_slideshow,omitempty"`
	PreviousValue         *int                      `json:"previous_value,omitempty"`
	DesiredValue          *int                      `json:"desired_value,omitempty"`
	RemoveUnknownApproved bool                      `json:"remove_unknown_approved,omitempty"`
	// Round is the shuffle round a select command belongs to.
	Round int `json:"round,omitempty"`
}

type Plan struct {
	CollectionGeneration string
	PolicyFingerprint    [sha256.Size]byte
	InventoryFingerprint [sha256.Size]byte
	CapacityLimited      bool
	Rotation             RotationStatus
	Commands             []commandIntent
	PruneBindings        []string
}

type tvIdentity struct {
	Address         string `json:"address"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	// DeviceID is the TV's stable identifier (DUID). When both sides know it,
	// it identifies the TV instead of its address, so an address change keeps
	// the record. Older state files omit it.
	DeviceID string `json:"device_id,omitempty"`
}

type Binding struct {
	Digest               string    `json:"digest"`
	ContentID            string    `json:"content_id"`
	Name                 string    `json:"name"`
	CollectionGeneration string    `json:"collection_generation"`
	ConfirmedAt          time.Time `json:"confirmed_at"`
}

type tombstone struct {
	Digest     string    `json:"digest,omitempty"`
	ContentID  string    `json:"content_id"`
	RecordedAt time.Time `json:"recorded_at"`
}

type capacityEvidence struct {
	Known      bool      `json:"known"`
	Maximum    int       `json:"maximum,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Probe      bool      `json:"probe,omitempty"`
	// Shortfalls counts consecutive storage-full rejections that occurred while
	// the TV held fewer items than Maximum. It bounds rotation deletions when a
	// TV limits storage by bytes rather than by item count.
	Shortfalls int `json:"shortfalls,omitempty"`
}

type inventoryFingerprint struct {
	Digest [sha256.Size]byte `json:"digest"`
}

type ReceiptSummary struct {
	CommandID   string          `json:"command_id"`
	Outcome     samsung.Outcome `json:"outcome"`
	ContentID   string          `json:"content_id,omitempty"`
	CompletedAt time.Time       `json:"completed_at"`
}

type pending struct {
	OperationID       string               `json:"operation_id"`
	CycleID           string               `json:"cycle_id"`
	CollectionGen     string               `json:"collection_generation"`
	PolicyFingerprint [sha256.Size]byte    `json:"policy_fingerprint"`
	InventoryBefore   inventoryFingerprint `json:"inventory_before"`
	Command           commandIntent        `json:"command"`
	Phase             phase                `json:"phase"`
	Receipt           *ReceiptSummary      `json:"receipt,omitempty"`
	// PreparedAt and InventoryBeforeIDs let an upload whose outcome is unknown
	// be proven from later inventory. Both are absent in older state files.
	PreparedAt         *time.Time `json:"prepared_at,omitempty"`
	InventoryBeforeIDs []string   `json:"inventory_before_ids,omitempty"`
}

type State struct {
	Version    int                  `json:"version"`
	TV         tvIdentity           `json:"tv"`
	Revision   uint64               `json:"revision"`
	Bindings   map[string]Binding   `json:"bindings"`
	Tombstones map[string]tombstone `json:"tombstones"`
	Capacity   capacityEvidence     `json:"capacity"`
	// LastShown records when desired artwork last left the TV, keyed by digest.
	// Rotation uses it to bring back the artwork that has been away longest.
	LastShown              map[string]time.Time `json:"last_shown,omitempty"`
	LegacyMigrationPending bool                 `json:"legacy_migration_pending,omitempty"`
	Pending                *pending             `json:"pending,omitempty"`
	// UnresolvedUpload is an upload whose outcome is still unknown. It blocks
	// artwork changes until inventory proves the outcome, while settings and
	// power control keep running.
	UnresolvedUpload *pending `json:"unresolved_upload,omitempty"`
	// Display records the pictures the manager has put on screen.
	Display           *displayState `json:"display,omitempty"`
	LastCompleteCycle string        `json:"last_complete_cycle_id,omitempty"`
	LastCollectionGen string        `json:"last_collection_generation,omitempty"`
	Checksum          string        `json:"checksum"`
}
