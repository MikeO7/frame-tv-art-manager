package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type executionInput struct {
	request     Request
	policy      Policy
	state       State
	observation samsung.Observation
	intent      CommandIntent
	pace        bool
}

func (s *service) execute(ctx context.Context, input executionInput) (State, samsung.Observation, error) {
	state, observation := input.state, input.observation
	operationID, err := s.ids.NewID()
	if err != nil {
		return state, observation, err
	}
	policyFingerprint, err := fingerprintPolicy(input.policy)
	if err != nil {
		return state, observation, err
	}
	prepared := cloneState(state)
	prepared.Revision++
	prepared.Pending = &Pending{
		OperationID: operationID, CycleID: input.request.CycleID, CollectionGen: input.request.Snapshot.Generation,
		PolicyFingerprint: policyFingerprint, InventoryBefore: InventoryFingerprint{Digest: observation.Inventory.Fingerprint},
		Command: input.intent, Phase: PhasePrepared,
	}
	if err := s.store.save(ctx, prepared); err != nil {
		return prepared, observation, err
	}
	if err := s.mutations.wait(ctx, input.pace); err != nil {
		cleared := cloneState(prepared)
		cleared.Revision++
		cleared.Pending = nil
		if saveErr := s.store.save(context.WithoutCancel(ctx), cleared); saveErr != nil {
			return prepared, observation, errors.Join(fmt.Errorf("pace Samsung mutation: %w", err), saveErr)
		}
		return cleared, observation, fmt.Errorf("pace Samsung mutation: %w", err)
	}
	command, err := samsungCommand(input.intent)
	if err != nil {
		return prepared, observation, err
	}
	receipt, applyErr := input.request.TV.Apply(ctx, observation.Authorization, command)
	return s.handleReceipt(ctx, prepared, observation, receipt, applyErr)
}

func receiptObservation(
	observation samsung.Observation,
	intent CommandIntent,
	receipt samsung.Receipt,
) samsung.Observation {
	switch intent.Kind {
	case CommandUpload:
		if !slices.Contains(observation.Inventory.ContentIDs, receipt.ContentID) {
			observation.Inventory.ContentIDs = append(observation.Inventory.ContentIDs, receipt.ContentID)
		}
		slices.Sort(observation.Inventory.ContentIDs)
	case CommandDeleteOwned, CommandDeleteUnknown:
		observation.Inventory.ContentIDs = slices.DeleteFunc(observation.Inventory.ContentIDs, func(id string) bool {
			return id == intent.ContentID
		})
	case CommandSlideshow:
		observation.Slideshow = samsung.SlideshowObservation{
			Setting: *intent.DesiredSlideshow, Known: true, ObservedAt: receipt.CompletedAt,
		}
	case CommandBrightness:
		observation.Brightness = samsung.SettingObservation{Value: *intent.DesiredValue, Known: true, ObservedAt: receipt.CompletedAt}
	case CommandPowerOff:
		observation.Power = samsung.PowerStateOff
		observation.Disposition = samsung.DispositionBlockedPowerOff
	case CommandWake:
		observation.Power = samsung.PowerStateOn
	}
	canonical, err := json.Marshal(observation.Inventory.ContentIDs)
	if err == nil {
		observation.Inventory.Fingerprint = sha256.Sum256(canonical)
	}
	return observation
}

// resolvePending settles the pending operation from fresh observation. It
// returns ErrRecoveryRequired when the observation cannot prove the outcome.
//
//nolint:gocognit // Recovery intentionally mirrors the command recovery matrix.
func resolvePending(state State, observation samsung.Observation, now time.Time) (State, error) {
	result := cloneState(state)
	pending := result.Pending
	if pending == nil {
		return result, nil
	}
	if pending.Phase == PhaseApplied && pending.Receipt != nil && pending.Receipt.Outcome == samsung.OutcomeApplied {
		return resolveAppliedReceipt(result, observation, now)
	}
	switch pending.Command.Kind {
	case CommandUpload:
		if !observation.Inventory.Known || pending.Receipt == nil || pending.Receipt.Outcome != samsung.OutcomeApplied ||
			strings.TrimSpace(pending.Receipt.ContentID) == "" ||
			!slices.Contains(observation.Inventory.ContentIDs, pending.Receipt.ContentID) {
			return state, ErrRecoveryRequired
		}
		result.Bindings[pending.Command.Digest] = uploadBinding(pending, now)
	case CommandDeleteOwned, CommandDeleteUnknown:
		if !observation.Inventory.Known {
			return state, ErrRecoveryRequired
		}
		if slices.Contains(observation.Inventory.ContentIDs, pending.Command.ContentID) {
			if pending.Phase == PhaseApplied {
				return state, ErrRecoveryRequired
			}
			result.Pending = nil
			return result, nil
		}
		if pending.Command.Kind == CommandDeleteOwned {
			delete(result.Bindings, pending.Command.Digest)
			recordShown(&result, pending.Command.Digest, now)
		}
		delete(result.Tombstones, pending.Command.ContentID)
	case CommandPowerOff, CommandWake:
		// Either known power state settles the command: the target state proves
		// it applied and the opposite state proves it did not.
		if pending.Phase != PhaseApplied &&
			observation.Power != samsung.PowerStateOn && observation.Power != samsung.PowerStateOff {
			return state, ErrRecoveryRequired
		}
	case CommandSlideshow:
		if !settingSettled(observation.Slideshow.Known, observation.Slideshow.Setting,
			pending.Command.PreviousSlideshow, pending.Command.DesiredSlideshow) {
			return state, ErrRecoveryRequired
		}
	case CommandBrightness:
		if !settingSettled(observation.Brightness.Known, observation.Brightness.Value,
			pending.Command.PreviousValue, pending.Command.DesiredValue) {
			return state, ErrRecoveryRequired
		}
	default:
		return state, ErrRecoveryRequired
	}
	result.Pending = nil
	return result, nil
}

func resolveAppliedReceipt(state State, observation samsung.Observation, now time.Time) (State, error) {
	pending := state.Pending
	switch pending.Command.Kind {
	case CommandUpload:
		if strings.TrimSpace(pending.Receipt.ContentID) == "" {
			return state, ErrRecoveryRequired
		}
		foldUploadBinding(&state, pending, observation, now)
	case CommandDeleteOwned:
		delete(state.Bindings, pending.Command.Digest)
		delete(state.Tombstones, pending.Command.ContentID)
		recordShown(&state, pending.Command.Digest, now)
	case CommandDeleteUnknown:
		delete(state.Tombstones, pending.Command.ContentID)
	case CommandSelect, CommandSlideshow, CommandBrightness, CommandPowerOff, CommandWake:
		// The Samsung applied receipt is issued only after command-specific
		// postcondition verification.
	default:
		return state, ErrRecoveryRequired
	}
	state.Pending = nil
	return state, nil
}

func foldUploadBinding(state *State, pending *Pending, observation samsung.Observation, confirmedAt time.Time) {
	reusedContentID := false
	for digest, binding := range state.Bindings {
		if digest != pending.Command.Digest && binding.ContentID == pending.Receipt.ContentID {
			delete(state.Bindings, digest)
			reusedContentID = true
		}
	}
	state.Bindings[pending.Command.Digest] = uploadBinding(pending, confirmedAt)
	if reusedContentID && observation.Inventory.Known &&
		pending.InventoryBefore.Digest == observation.Inventory.Fingerprint &&
		slices.Contains(observation.Inventory.ContentIDs, pending.Receipt.ContentID) {
		state.Capacity = CapacityEvidence{
			Known: true, Maximum: len(observation.Inventory.ContentIDs), ObservedAt: confirmedAt,
		}
	}
}

func uploadBinding(pending *Pending, confirmedAt time.Time) Binding {
	return Binding{
		Digest: pending.Command.Digest, ContentID: pending.Receipt.ContentID, Name: pending.Command.Name,
		CollectionGeneration: pending.CollectionGen, ConfirmedAt: confirmedAt,
	}
}

// settingSettled reports whether a known observed setting proves the outcome
// of a setting command: its desired value or its retained previous value. Any
// other value is ambiguous.
func settingSettled[T comparable](known bool, observed T, previous, desired *T) bool {
	return known && previous != nil && desired != nil && (observed == *desired || observed == *previous)
}

func receiptSummary(receipt samsung.Receipt) *ReceiptSummary {
	return &ReceiptSummary{
		CommandID: receipt.CommandID, Outcome: receipt.Outcome,
		ContentID: receipt.ContentID, CompletedAt: receipt.CompletedAt,
	}
}
