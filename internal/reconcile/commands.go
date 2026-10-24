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

func (s *service) effectivePolicy(request Policy) Policy {
	if request == (Policy{}) {
		return s.policy
	}
	return normalizePolicy(request)
}

const baseCapabilities = samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory

func initialObservationCapabilities(policy Policy) samsung.CapabilitySet {
	if policy.Power == PowerOn {
		return samsung.CapabilityRemotePower
	}
	return convergenceObservationCapabilities(policy)
}

func convergenceObservationCapabilities(policy Policy) samsung.CapabilitySet {
	required := baseCapabilities
	if policy.Slideshow.Mode != PolicyPreserve {
		required |= samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite
	}
	if policy.Brightness.Mode != PolicyPreserve {
		required |= samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite
	}
	if policy.Power == PowerOff {
		required |= samsung.CapabilityRemotePower
	}
	return required
}

func requiredCapabilities(command CommandIntent) samsung.CapabilitySet {
	required := baseCapabilities
	switch command.Kind {
	case CommandWake:
		return samsung.CapabilityRemotePower
	case CommandUpload:
		required |= samsung.CapabilityImageUpload
	case CommandDeleteOwned, CommandDeleteUnknown:
		required |= samsung.CapabilityImageDeletion
	case CommandSelect:
		required |= samsung.CapabilityImageSelection
	case CommandSlideshow:
		required |= samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite
	case CommandBrightness:
		required |= samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite
	case CommandPowerOff:
		required |= samsung.CapabilityRemotePower
	}
	return required
}

// sameIntent compares intents by value, including the settings behind their
// optional pointers.
func sameIntent(left, right CommandIntent) bool {
	return reflect.DeepEqual(left, right)
}

func withoutCommand(commands []CommandIntent, kind CommandKind) []CommandIntent {
	return slices.DeleteFunc(slices.Clone(commands), func(command CommandIntent) bool {
		return command.Kind == kind
	})
}

func samsungCommand(intent CommandIntent) (samsung.Command, error) {
	switch intent.Kind {
	case CommandUpload:
		digestBytes, err := hex.DecodeString(intent.Digest)
		if err != nil || len(digestBytes) != sha256.Size {
			return nil, errors.New("upload intent digest is invalid")
		}
		var digest [sha256.Size]byte
		copy(digest[:], digestBytes)
		return samsung.Upload{
			Path: intent.Path, Name: intent.Name, FileType: string(intent.FileType), Matte: intent.Matte,
			Digest: digest, Size: intent.Size,
		}, nil
	case CommandDeleteOwned, CommandDeleteUnknown:
		return samsung.Delete{ContentID: intent.ContentID}, nil
	case CommandSelect:
		return samsung.Select{ContentID: intent.ContentID}, nil
	case CommandSlideshow:
		return samsung.ConfigureSlideshow{Previous: *intent.PreviousSlideshow, Desired: *intent.DesiredSlideshow}, nil
	case CommandBrightness:
		return samsung.ConfigureBrightness{PreviousValue: *intent.PreviousValue, Value: *intent.DesiredValue}, nil
	case CommandPowerOff:
		return samsung.PowerOff{}, nil
	case CommandWake:
		return samsung.Wake{}, nil
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
