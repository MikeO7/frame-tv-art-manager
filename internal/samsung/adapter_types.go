package samsung

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"time"
)

// PowerState is an observed TV power fact. Its zero value is unknown.
type PowerState uint8

const (
	PowerStateUnknown PowerState = iota
	PowerStateOn
	PowerStateOff
)

// ArtModeState is an observed Art Mode fact. Its zero value is unknown.
type ArtModeState uint8

const (
	ArtModeUnknown ArtModeState = iota
	ArtModeOn
	ArtModeOff
)

// Support records whether a capability has been observed. Its zero value is
// unknown and never implies support.
type Support uint8

const (
	SupportUnknown Support = iota
	SupportSupported
	SupportUnsupported
)

// ConnectionState describes the read-only adapter connection lifecycle.
type ConnectionState uint8

const (
	ConnectionDisconnected ConnectionState = iota
	ConnectionConnecting
	ConnectionReady
	ConnectionBackingOff
	ConnectionAuthRequired
)

// Disposition states whether an observation is usable for planning. Unknown
// is deliberately the zero value so an incomplete observation fails closed.
type Disposition uint8

const (
	DispositionUnsafeUnknown Disposition = iota
	DispositionEligible
	DispositionBlockedPowerOff
	DispositionBlockedNotArtMode
	DispositionBlockedBackoff
	DispositionBlockedQuietGate
)

// CapabilitySet identifies behaviors required by a reconciliation plan.
type CapabilitySet uint16

const (
	CapabilityArtStateObservation CapabilitySet = 1 << iota
	CapabilityUserArtInventory
	CapabilityImageUpload
	CapabilityImageDeletion
	CapabilityImageSelection
	CapabilitySlideshowRead
	CapabilitySlideshowWrite
	CapabilityBrightnessRead
	CapabilityBrightnessWrite
	CapabilityRemotePower
)

// Capabilities contains independently observed behavior support.
type Capabilities struct {
	ArtStateObservation Support
	UserArtInventory    Support
	ImageUpload         Support
	ImageDeletion       Support
	ImageSelection      Support
	SlideshowRead       Support
	SlideshowWrite      Support
	BrightnessRead      Support
	BrightnessWrite     Support
	RemotePower         Support
}

func (c Capabilities) supports(required CapabilitySet) bool {
	checks := []struct {
		capability CapabilitySet
		support    Support
	}{
		{CapabilityArtStateObservation, c.ArtStateObservation},
		{CapabilityUserArtInventory, c.UserArtInventory},
		{CapabilityImageUpload, c.ImageUpload},
		{CapabilityImageDeletion, c.ImageDeletion},
		{CapabilityImageSelection, c.ImageSelection},
		{CapabilitySlideshowRead, c.SlideshowRead},
		{CapabilitySlideshowWrite, c.SlideshowWrite},
		{CapabilityBrightnessRead, c.BrightnessRead},
		{CapabilityBrightnessWrite, c.BrightnessWrite},
		{CapabilityRemotePower, c.RemotePower},
	}
	for _, check := range checks {
		if required&check.capability != 0 && check.support != SupportSupported {
			return false
		}
	}
	return true
}

// TVIdentity contains device facts obtained from the TV rather than inferred
// from its configured address.
type TVIdentity struct {
	Address         string
	Model           string
	FirmwareVersion string
	Known           bool
}

// Inventory is a trustworthy observation of user-managed artwork. Known is
// true for an explicit empty list and false for absent or ambiguous payloads.
type Inventory struct {
	CategoryID  string
	ContentIDs  []string
	Fingerprint [sha256.Size]byte
	Known       bool
	ObservedAt  time.Time
}

// ObserveRequest binds an observation to one sync cycle and Collection
// Snapshot generation.
type ObserveRequest struct {
	CycleID              string
	CollectionGeneration string
	Required             CapabilitySet
	DryRun               bool
}

// Authorization is an opaque, single-use planning token whose private facts
// are revalidated immediately before a protocol write.
type Authorization struct {
	adapter              *adapter
	connectionGeneration uint64
	cycleID              string
	collectionGeneration string
	inventoryFingerprint [sha256.Size]byte
	required             CapabilitySet
	dryRun               bool
}

func (a Authorization) isZero() bool { return a.adapter == nil }

// Observation is an immutable, normalized view of the facts successfully
// received during one read-only observation.
type Observation struct {
	TV            TVIdentity
	Connection    ConnectionState
	Power         PowerState
	ArtMode       ArtModeState
	Inventory     Inventory
	Slideshow     SlideshowObservation
	Brightness    SettingObservation
	Capabilities  Capabilities
	ObservedAt    time.Time
	Disposition   Disposition
	Authorization Authorization
}

// SlideshowObservation is a complete slideshow value proven by a matched TV
// read in this cycle.
type SlideshowObservation struct {
	Setting    SlideshowSetting
	Known      bool
	ObservedAt time.Time
}

// SettingObservation is an integer value proven by a matched TV read in this
// cycle. It is used for brightness.
type SettingObservation struct {
	Value      int
	Known      bool
	ObservedAt time.Time
}

// Command is sealed to the Samsung package so arbitrary wire messages cannot
// cross the adapter's safety boundary.
type Command interface {
	isSamsungCommand()
}

// Wake is the explicit wake command. Observe never sends it.
type Wake struct{}

func (Wake) isSamsungCommand() {}

// PowerOff is the explicit guarded power-off command.
type PowerOff struct{}

func (PowerOff) isSamsungCommand() {}

// Receipt reports a command outcome.
type Receipt struct {
	CommandID   string
	Outcome     Outcome
	ContentID   string
	CompletedAt time.Time
}

// Adapter is the only Samsung surface intended for reconciliation code.
type Adapter interface {
	Observe(context.Context, ObserveRequest) (Observation, error)
	Apply(context.Context, Authorization, Command) (Receipt, error)
	Close(context.Context) error
}

// Clock provides deterministic operational time without package globals.
type Clock interface {
	Now() time.Time
}

// Config contains settings owned by one Samsung adapter.
type Config struct {
	Address        string
	MAC            net.HardwareAddr
	ClientName     string
	TokenPath      string
	VerifyTLS      bool
	QuietGate      bool
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	GateTimeout    time.Duration
	BackoffBase    time.Duration
	BackoffMaximum time.Duration
}

// Dependencies contains operational dependencies shared by one adapter.
type Dependencies struct {
	Clock  Clock
	Random io.Reader
	Logger *slog.Logger
}
