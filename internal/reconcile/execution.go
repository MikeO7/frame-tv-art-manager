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
		observation.Inventory.ContentIDs = append(observation.Inventory.ContentIDs, receipt.ContentID)
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

//nolint:funlen,gocognit,gocyclo // Recovery intentionally mirrors the command recovery matrix.
func resolvePending(state State, observation samsung.Observation, now time.Time) (State, bool, error) {
	result := cloneState(state)
	pending := result.Pending
	if pending == nil {
		return result, true, nil
	}
	if pending.Phase == PhaseApplied && pending.Receipt != nil && pending.Receipt.Outcome == samsung.OutcomeApplied {
		return resolveAppliedReceipt(result, now)
	}
	contains := func(contentID string) bool {
		for _, observed := range observation.Inventory.ContentIDs {
			if observed == contentID {
				return true
			}
		}
		return false
	}
	switch pending.Command.Kind {
	case CommandUpload:
		if !observation.Inventory.Known || pending.Receipt == nil || pending.Receipt.Outcome != samsung.OutcomeApplied ||
			strings.TrimSpace(pending.Receipt.ContentID) == "" || !contains(pending.Receipt.ContentID) {
			return state, false, ErrRecoveryRequired
		}
		result.Bindings[pending.Command.Digest] = uploadBinding(pending, now)
	case CommandDeleteOwned, CommandDeleteUnknown:
		if !observation.Inventory.Known {
			return state, false, ErrRecoveryRequired
		}
		if contains(pending.Command.ContentID) {
			if pending.Phase == PhaseApplied {
				return state, false, ErrRecoveryRequired
			}
			result.Pending = nil
			return result, true, nil
		}
		if pending.Command.Kind == CommandDeleteOwned {
			delete(result.Bindings, pending.Command.Digest)
		}
		delete(result.Tombstones, pending.Command.ContentID)
	case CommandPowerOff:
		if observation.Power == samsung.PowerStateOn && pending.Phase != PhaseApplied {
			result.Pending = nil
			return result, true, nil
		}
		if observation.Power != samsung.PowerStateOff && pending.Phase != PhaseApplied {
			return state, false, ErrRecoveryRequired
		}
	case CommandWake:
		if observation.Power == samsung.PowerStateOff && pending.Phase != PhaseApplied {
			result.Pending = nil
			return result, true, nil
		}
		if observation.Power != samsung.PowerStateOn && pending.Phase != PhaseApplied {
			return state, false, ErrRecoveryRequired
		}
	case CommandSlideshow:
		return resolveSlideshowPending(state, result, observation.Slideshow)
	case CommandBrightness:
		return resolveSettingPending(state, result, observation.Brightness)
	case CommandSelect:
		return state, false, ErrRecoveryRequired
	default:
		return state, false, ErrRecoveryRequired
	}
	result.Pending = nil
	return result, true, nil
}

func resolveAppliedReceipt(state State, now time.Time) (State, bool, error) {
	pending := state.Pending
	switch pending.Command.Kind {
	case CommandUpload:
		if strings.TrimSpace(pending.Receipt.ContentID) == "" {
			return state, false, ErrRecoveryRequired
		}
		state.Bindings[pending.Command.Digest] = uploadBinding(pending, now)
	case CommandDeleteOwned:
		delete(state.Bindings, pending.Command.Digest)
		delete(state.Tombstones, pending.Command.ContentID)
	case CommandDeleteUnknown:
		delete(state.Tombstones, pending.Command.ContentID)
	case CommandSelect, CommandSlideshow, CommandBrightness, CommandPowerOff, CommandWake:
		// The Samsung applied receipt is issued only after command-specific
		// postcondition verification.
	default:
		return state, false, ErrRecoveryRequired
	}
	state.Pending = nil
	return state, true, nil
}

func uploadBinding(pending *Pending, confirmedAt time.Time) Binding {
	return Binding{
		Digest: pending.Command.Digest, ContentID: pending.Receipt.ContentID, Name: pending.Command.Name,
		CollectionGeneration: pending.CollectionGen, ConfirmedAt: confirmedAt,
	}
}

func resolveSettingPending(
	original State,
	result State,
	observation samsung.SettingObservation,
) (State, bool, error) {
	if !observation.Known || result.Pending.Command.PreviousValue == nil || result.Pending.Command.DesiredValue == nil {
		return original, false, ErrRecoveryRequired
	}
	switch observation.Value {
	case *result.Pending.Command.DesiredValue, *result.Pending.Command.PreviousValue:
		result.Pending = nil
		return result, true, nil
	default:
		return original, false, ErrRecoveryRequired
	}
}

func resolveSlideshowPending(
	original State,
	result State,
	observation samsung.SlideshowObservation,
) (State, bool, error) {
	previous := result.Pending.Command.PreviousSlideshow
	desired := result.Pending.Command.DesiredSlideshow
	if !observation.Known || previous == nil || desired == nil {
		return original, false, ErrRecoveryRequired
	}
	if observation.Setting == *desired || observation.Setting == *previous {
		result.Pending = nil
		return result, true, nil
	}
	return original, false, ErrRecoveryRequired
}

func receiptSummary(receipt samsung.Receipt) *ReceiptSummary {
	return &ReceiptSummary{
		CommandID: receipt.CommandID, Outcome: receipt.Outcome,
		ContentID: receipt.ContentID, CompletedAt: receipt.CompletedAt,
	}
}
