package samsung

import (
	"context"
	"crypto/sha256"
)

func (a *adapter) newAuthorization(
	request ObserveRequest,
	connectionGeneration uint64,
	inventoryFingerprint [sha256.Size]byte,
) Authorization {
	return Authorization{
		adapter:              a,
		connectionGeneration: connectionGeneration,
		cycleID:              request.CycleID,
		collectionGeneration: request.CollectionGeneration,
		inventoryFingerprint: inventoryFingerprint,
		required:             request.Required,
		dryRun:               request.DryRun,
	}
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
	// Samsung's art API does not expose a reliable current-selection readback,
	// so selection cannot be authorized with a verifiable postcondition.
	observation.Capabilities.ImageSelection = SupportUnsupported
	observation.Capabilities.RemotePower = SupportSupported
	if request.Required&(CapabilitySlideshowRead|CapabilitySlideshowWrite) != 0 {
		value, err := transport.Slideshow(ctx)
		if err != nil {
			return err
		}
		observation.Slideshow = SlideshowObservation{Setting: value, Known: true, ObservedAt: observation.ObservedAt}
		observation.Capabilities.SlideshowRead = SupportSupported
		observation.Capabilities.SlideshowWrite = SupportSupported
	}
	if request.Required&(CapabilityBrightnessRead|CapabilityBrightnessWrite) != 0 {
		value, err := transport.Brightness(ctx)
		if err != nil {
			return err
		}
		observation.Brightness = SettingObservation{Value: value, Known: true, ObservedAt: observation.ObservedAt}
		observation.Capabilities.BrightnessRead = SupportSupported
		observation.Capabilities.BrightnessWrite = SupportSupported
	}
	return nil
}
