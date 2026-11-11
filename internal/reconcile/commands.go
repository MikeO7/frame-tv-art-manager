package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func validateRequest(cycleID string, tv samsung.Adapter, snapshot collection.Snapshot, policy Policy, dryRun bool) error {
	if strings.TrimSpace(cycleID) == "" {
		return errors.New("reconciliation cycle ID is required")
	}
	if tv == nil {
		return errors.New("samsung adapter is required")
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.DryRun && !dryRun {
		return errors.New("a projected collection snapshot cannot authorize TV mutation")
	}
	return validatePolicy(policy)
}

const baseCapabilities = samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory

func initialObservationCapabilities(policy Policy) samsung.CapabilitySet {
	if policy.Power == PowerOn {
		return samsung.CapabilityRemotePower
	}
	return convergenceObservationCapabilities(policy)
}

// settingCapabilities lists the settings a policy manages. They are observed
// as optional capabilities: a TV that does not answer one still converges
// artwork and its other settings.
func settingCapabilities(policy Policy) samsung.CapabilitySet {
	var optional samsung.CapabilitySet
	if policy.Slideshow.Mode != PolicyPreserve {
		optional |= samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite
	}
	if policy.Brightness.Mode != PolicyPreserve {
		optional |= samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite
	}
	if policy.Show.Interval > 0 {
		optional |= samsung.CapabilityImageSelection
	}
	return optional
}

func convergenceObservationCapabilities(policy Policy) samsung.CapabilitySet {
	required := baseCapabilities
	if policy.Power == PowerOff {
		required |= samsung.CapabilityRemotePower
	}
	return required
}

func requiredCapabilities(command commandIntent) samsung.CapabilitySet {
	required := baseCapabilities
	switch command.Kind {
	case CommandUpload:
		required |= samsung.CapabilityImageUpload
	case CommandDeleteOwned, CommandDeleteUnknown:
		required |= samsung.CapabilityImageDeletion
	case commandSelect:
		required |= samsung.CapabilityImageSelection
	case commandSlideshow:
		required |= samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite
	case CommandBrightness:
		required |= samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite
	case commandPowerOff:
		required |= samsung.CapabilityRemotePower
	}
	return required
}

// sameIntent compares intents by value, including the settings behind their
// optional pointers.
func sameIntent(left, right commandIntent) bool {
	return reflect.DeepEqual(left, right)
}

func withoutCommand(commands []commandIntent, kind CommandKind) []commandIntent {
	return slices.DeleteFunc(slices.Clone(commands), func(command commandIntent) bool {
		return command.Kind == kind
	})
}

func samsungCommand(intent commandIntent) (samsung.Command, error) {
	switch intent.Kind {
	case CommandUpload:
		digestBytes, err := hex.DecodeString(intent.Digest)
		if err != nil || len(digestBytes) != sha256.Size {
			return nil, errors.New("upload intent digest is invalid")
		}
		var digest [sha256.Size]byte
		copy(digest[:], digestBytes)
		return samsung.Upload{
			Path: intent.Path, Name: intent.Name, FileType: string(intent.FileType), Matte: intent.Matte, PortraitMatte: intent.PortraitMatte,
			Digest: digest, Size: intent.Size,
		}, nil
	case CommandDeleteOwned, CommandDeleteUnknown:
		return samsung.Delete{ContentID: intent.ContentID}, nil
	case commandSelect:
		return samsung.Select{ContentID: intent.ContentID}, nil
	case commandSlideshow:
		return samsung.ConfigureSlideshow{Previous: *intent.PreviousSlideshow, Desired: *intent.DesiredSlideshow}, nil
	case CommandBrightness:
		return samsung.ConfigureBrightness{PreviousValue: *intent.PreviousValue, Value: *intent.DesiredValue}, nil
	case commandPowerOff:
		return samsung.PowerOff{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedIntent, intent.Kind)
	}
}

func statusForError(err error) Status {
	switch {
	case errors.Is(err, ErrPersistenceUnknown):
		return StatusPersistenceUnknown
	case errors.Is(err, ErrRecoveryRequired):
		return StatusRecoveryRequired
	case errors.Is(err, ErrUnsupportedIntent):
		return StatusUnsupported
	case errors.Is(err, samsung.ErrStorageFull):
		return StatusStorageFull
	default:
		return StatusNotApplied
	}
}
