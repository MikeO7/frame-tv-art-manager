package samsung

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"syscall"
)

// observeReachable finishes an observation of a TV whose REST endpoint
// answered with a verified Frame identity.
func (a *adapter) observeReachable(
	ctx context.Context,
	observation Observation,
	request ObserveRequest,
	device DeviceInfo,
	connectionGeneration uint64,
) (Observation, error) {
	observation.Connection = ConnectionConnecting
	state, err := a.readPowerState(ctx, device, request.DryRun)
	if errors.Is(err, ErrGateFailed) {
		observation.Connection = ConnectionDisconnected
		observation.Disposition = DispositionBlockedQuietGate
		return a.settleObservation(observation, request), nil
	}
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe operational state", err)
	}
	observation.Power = state.Power
	observation.ArtMode = state.ArtMode
	observation.Connection = ConnectionDisconnected
	if state.connected {
		observation.Connection = ConnectionReady
	}
	if state.ArtMode != ArtModeUnknown {
		observation.Capabilities.ArtStateObservation = SupportSupported
	}
	if state.Power == PowerStateOff {
		return a.knownPowerOffObservation(observation, request, connectionGeneration), nil
	}
	if state.ArtMode == ArtModeOff {
		observation.Disposition = DispositionBlockedNotArtMode
		return a.settleObservation(observation, request), nil
	}

	rawInventory, err := a.transport.Inventory(ctx)
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe inventory", err)
	}
	inventory, err := normalizeInventory(rawInventory, observation.ObservedAt)
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe inventory", err)
	}
	observation.Inventory = inventory
	observation.Capabilities.UserArtInventory = SupportSupported
	if err := a.observeMutationCapabilities(ctx, request, &observation); err != nil {
		return a.failObservation(ctx, observation, request, "observe command capabilities", err)
	}
	if !observation.Capabilities.supports(request.Required) {
		a.publishAuthorizationFacts(observation, request)
		return cloneObservation(observation), nil
	}

	observation.Disposition = DispositionEligible
	observation.Authorization = a.newAuthorization(request, connectionGeneration, inventory.Fingerprint)
	return a.settleObservation(observation, request), nil
}

// settleObservation completes a successful observation: it clears backoff and
// publishes the facts a later authorization must match.
func (a *adapter) settleObservation(observation Observation, request ObserveRequest) Observation {
	a.resetFailures()
	a.publishAuthorizationFacts(observation, request)
	return cloneObservation(observation)
}

func (a *adapter) knownPowerOffObservation(
	observation Observation,
	request ObserveRequest,
	connectionGeneration uint64,
) Observation {
	observation.Power = PowerStateOff
	observation.Disposition = DispositionBlockedPowerOff
	if request.Required == CapabilityRemotePower && a.canWake() {
		observation.Capabilities.RemotePower = SupportSupported
		observation.Authorization = a.newAuthorization(request, connectionGeneration, [sha256.Size]byte{})
	}
	return a.settleObservation(observation, request)
}

// presumedStandbyObservation reports a TV whose REST endpoint does not answer.
// Frame TVs in deep standby drop off the network except for Wake-on-LAN, so
// the power state stays unknown, but a wake (a broadcast that is harmless to a
// TV that is already on) is authorized. Nothing else is.
func (a *adapter) presumedStandbyObservation(
	observation Observation,
	request ObserveRequest,
	connectionGeneration uint64,
) Observation {
	observation.PresumedStandby = true
	observation.Disposition = DispositionUnsafeUnknown
	observation.Capabilities.RemotePower = SupportSupported
	observation.Authorization = a.newAuthorization(request, connectionGeneration, [sha256.Size]byte{})
	observation.Authorization.presumedStandby = true
	a.publishAuthorizationFacts(observation, request)
	return cloneObservation(observation)
}

func (a *adapter) canWake() bool {
	_, supportsMutation := a.transport.(mutationTransport)
	return supportsMutation && len(a.config.MAC) == 6
}

// unreachableError reports a transport failure where the TV did not answer at
// all, as opposed to an answer that was refused or malformed.
func unreachableError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// capabilityProbe reads one setting during observation. Each optional probe
// is independent: a TV that rejects one setting request still reconciles
// artwork and the settings it does answer.
type capabilityProbe struct {
	name       string
	capability CapabilitySet
	read       func(context.Context, mutationTransport, *Observation) error
	support    func(*Capabilities, Support)
}

func (a *adapter) observeMutationCapabilities(
	ctx context.Context,
	request ObserveRequest,
	observation *Observation,
) error {
	transport, ok := a.transport.(mutationTransport)
	if !ok {
		return nil
	}
	observation.Capabilities.ImageUpload = SupportSupported
	observation.Capabilities.ImageDeletion = SupportSupported
	observation.Capabilities.RemotePower = SupportSupported
	wanted := request.Required | request.Optional
	for _, probe := range capabilityProbes() {
		if wanted&probe.capability == 0 {
			continue
		}
		err := probe.read(ctx, transport, observation)
		if err == nil {
			probe.support(&observation.Capabilities, SupportSupported)
			continue
		}
		probe.support(&observation.Capabilities, SupportUnsupported)
		if request.Required&probe.capability != 0 || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotConnected) {
			return err
		}
		a.logger.Warn("TV did not answer an optional setting; leaving it unmanaged this cycle",
			"setting", probe.name, "error", err)
	}
	return nil
}

func capabilityProbes() []capabilityProbe {
	return []capabilityProbe{
		{
			// Selection is authorized only when get_current_artwork answers,
			// so its postcondition can be verified.
			name: "current artwork", capability: CapabilityImageSelection,
			read: func(ctx context.Context, transport mutationTransport, observation *Observation) error {
				contentID, err := transport.CurrentArtwork(ctx)
				if err != nil {
					return err
				}
				observation.CurrentArtwork = contentID
				return nil
			},
			support: func(capabilities *Capabilities, support Support) { capabilities.ImageSelection = support },
		},
		{
			name: "slideshow", capability: CapabilitySlideshowRead | CapabilitySlideshowWrite,
			read: func(ctx context.Context, transport mutationTransport, observation *Observation) error {
				value, err := transport.Slideshow(ctx)
				if err != nil {
					return err
				}
				observation.Slideshow = SlideshowObservation{Setting: value, Known: true, ObservedAt: observation.ObservedAt}
				return nil
			},
			support: func(capabilities *Capabilities, support Support) {
				capabilities.SlideshowRead, capabilities.SlideshowWrite = support, support
			},
		},
		{
			name: "brightness", capability: CapabilityBrightnessRead | CapabilityBrightnessWrite,
			read: func(ctx context.Context, transport mutationTransport, observation *Observation) error {
				reading, err := transport.Brightness(ctx)
				if err != nil {
					return err
				}
				observation.Brightness = SettingObservation{
					Value: reading.Value, Min: reading.Min, Max: reading.Max, Known: true, ObservedAt: observation.ObservedAt,
				}
				return nil
			},
			support: func(capabilities *Capabilities, support Support) {
				capabilities.BrightnessRead, capabilities.BrightnessWrite = support, support
			},
		},
	}
}
