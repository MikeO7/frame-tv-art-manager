package samsung

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"os"
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
	// DeviceID is the TV's hardware identity (REST duid, id, or wifiMac). It
	// survives DHCP address changes and is empty when the TV reported none.
	DeviceID string
	Known    bool
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
	// Optional capabilities are observed when the TV supports them, but an
	// unsupported optional setting never makes the observation ineligible.
	Optional CapabilitySet
	DryRun   bool
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
	presumedStandby      bool
}

// Observation is an immutable, normalized view of the facts successfully
// received during one read-only observation.
type Observation struct {
	TV         TVIdentity
	Connection ConnectionState
	Power      PowerState
	ArtMode    ArtModeState
	Inventory  Inventory
	Slideshow  SlideshowObservation
	Brightness SettingObservation
	// CurrentArtwork is the content ID on screen, when the TV reported it.
	CurrentArtwork string
	Capabilities   Capabilities
	ObservedAt     time.Time
	Disposition    Disposition
	Authorization  Authorization
	// PresumedStandby reports that REST did not answer a wake observation.
	// Power stays unknown; only a Wake is authorized.
	PresumedStandby bool
}

// SlideshowObservation is a complete slideshow value proven by a matched TV
// read in this cycle.
type SlideshowObservation struct {
	Setting    SlideshowSetting
	Known      bool
	ObservedAt time.Time
}

// SettingObservation is an integer value proven by a matched TV read in this
// cycle. It is used for brightness. Min and Max are the range the TV reported
// for the setting (0 to 10 on Frame TVs).
type SettingObservation struct {
	Value      int
	Min        int
	Max        int
	Known      bool
	ObservedAt time.Time
}

// BrightnessReading is one Art Mode brightness read with the TV's range.
type BrightnessReading struct {
	Value int
	Min   int
	Max   int
}

// Clamp limits value to the TV's reported range.
func (reading BrightnessReading) Clamp(value int) int {
	return min(max(value, reading.Min), reading.Max)
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
	// WakeBroadcast replaces the default Wake-on-LAN broadcast addresses when
	// set (TV_WOL_BROADCAST).
	WakeBroadcast string
}

// Dependencies contains operational dependencies shared by one adapter.
type Dependencies struct {
	Clock  Clock
	Random io.Reader
	Logger *slog.Logger
}

// Upload binds a committed artwork file's immutable metadata to one command.
type Upload struct {
	Path     string
	Name     string
	FileType string
	Matte    string
	// PortraitMatte is the matte the TV uses when it shows the artwork in
	// portrait orientation. Empty uses Matte.
	PortraitMatte string
	Digest        [sha256.Size]byte
	Size          int64
}

func (Upload) isSamsungCommand() {}

// Delete removes exactly one freshly observed content ID.
type Delete struct{ ContentID string }

func (Delete) isSamsungCommand() {}

// Select displays exactly one freshly observed content ID.
type Select struct{ ContentID string }

func (Select) isSamsungCommand() {}

// ConfigureSlideshow changes a complete known slideshow setting.
type ConfigureSlideshow struct {
	Previous SlideshowSetting
	Desired  SlideshowSetting
}

func (ConfigureSlideshow) isSamsungCommand() {}

// ConfigureBrightness changes a known previous level to Value.
type ConfigureBrightness struct {
	PreviousValue int
	Value         int
}

func (ConfigureBrightness) isSamsungCommand() {}

type mutationTransport interface {
	observationTransport
	artMutationTransport
	settingMutationTransport
	powerMutationTransport
}

type artMutationTransport interface {
	Upload(context.Context, preparedUpload) (string, error)
	Delete(context.Context, string) error
	Select(context.Context, string) error
	CurrentArtwork(context.Context) (string, error)
	MatteCatalog(context.Context) (matteCatalog, error)
}

type settingMutationTransport interface {
	Slideshow(context.Context) (SlideshowSetting, error)
	ConfigureSlideshow(context.Context, SlideshowSetting) error
	Brightness(context.Context) (BrightnessReading, error)
	ConfigureBrightness(context.Context, int) error
}

type powerMutationTransport interface {
	Wake(context.Context) error
	PowerOff(context.Context) error
}

type preparedUpload struct {
	file          *os.File
	fileType      string
	matte         string
	portraitMatte string
	size          int64
	digest        [sha256.Size]byte
	// existing holds the content IDs on the TV before the upload, so an
	// image_added event without a request ID is matched only to a new ID.
	existing map[string]struct{}
}
