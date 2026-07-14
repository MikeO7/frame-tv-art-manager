package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

func observe(ctx context.Context, tv samsung.Adapter, request samsung.ObserveRequest) (samsung.Observation, error) {
	return tv.Observe(ctx, request)
}

func baseCapabilities() samsung.CapabilitySet {
	return samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory
}

func initialObservationCapabilities(policy Policy) samsung.CapabilitySet {
	if policy.Power == PowerOn {
		return samsung.CapabilityRemotePower
	}
	return convergenceObservationCapabilities(policy)
}

func convergenceObservationCapabilities(policy Policy) samsung.CapabilitySet {
	required := baseCapabilities()
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

func requiredCapabilities(commands []CommandIntent) samsung.CapabilitySet {
	if len(commands) == 1 && commands[0].Kind == CommandWake {
		return samsung.CapabilityRemotePower
	}
	required := baseCapabilities()
	for _, command := range commands {
		switch command.Kind {
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
		case CommandPowerOff, CommandWake:
			required |= samsung.CapabilityRemotePower
		}
	}
	return required
}

func firstUnsupported(commands []CommandIntent) *CommandIntent {
	for index := range commands {
		switch commands[index].Kind {
		case CommandUpload, CommandDeleteOwned, CommandDeleteUnknown, CommandSelect,
			CommandSlideshow, CommandBrightness, CommandPowerOff, CommandWake:
		default:
			return &commands[index]
		}
	}
	return nil
}

func sameIntent(left, right CommandIntent) bool {
	if !sameIntentPayload(left, right) {
		return false
	}
	return equalOptionalSlideshow(left.PreviousSlideshow, right.PreviousSlideshow) &&
		equalOptionalSlideshow(left.DesiredSlideshow, right.DesiredSlideshow) &&
		equalOptionalInt(left.PreviousValue, right.PreviousValue) &&
		equalOptionalInt(left.DesiredValue, right.DesiredValue)
}

func sameIntentPayload(left, right CommandIntent) bool {
	return left.Kind == right.Kind && left.Digest == right.Digest && left.ContentID == right.ContentID &&
		left.Name == right.Name && left.Path == right.Path && left.FileType == right.FileType &&
		left.Size == right.Size && left.Matte == right.Matte &&
		left.RemoveUnknownApproved == right.RemoveUnknownApproved
}

func withoutCommand(commands []CommandIntent, kind CommandKind) []CommandIntent {
	return slices.DeleteFunc(slices.Clone(commands), func(command CommandIntent) bool {
		return command.Kind == kind
	})
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalOptionalSlideshow(left, right *samsung.SlideshowSetting) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
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
	default:
		return StatusNotApplied
	}
}

func dryRunStatus(dryRun bool) Status {
	if dryRun {
		return StatusIncompleteDryRun
	}
	return StatusComplete
}
