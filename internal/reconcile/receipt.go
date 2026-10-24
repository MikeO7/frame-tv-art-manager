package reconcile

import (
	"context"
	"errors"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func (s *service) handleReceipt(
	ctx context.Context,
	state State,
	observation samsung.Observation,
	receipt samsung.Receipt,
	applyErr error,
) (State, samsung.Observation, error) {
	switch receipt.Outcome {
	case samsung.OutcomeApplied:
		return s.handleAppliedReceipt(ctx, state, observation, receipt)
	case samsung.OutcomeUnknown:
		state.Revision++
		state.Pending.Phase = PhaseOutcomeUnknown
		state.Pending.Receipt = receiptSummary(receipt)
		if err := s.store.save(context.WithoutCancel(ctx), state); err != nil {
			return state, observation, errors.Join(ErrRecoveryRequired, applyErr, err)
		}
		return state, observation, errors.Join(ErrRecoveryRequired, applyErr)
	case samsung.OutcomeNotAttempted, samsung.OutcomeNotApplied:
		return s.handleDefiniteRejection(ctx, state, observation, receipt, applyErr)
	default:
		return state, observation, errors.New("samsung adapter returned an invalid command outcome")
	}
}

func (s *service) handleAppliedReceipt(
	ctx context.Context,
	state State,
	observation samsung.Observation,
	receipt samsung.Receipt,
) (State, samsung.Observation, error) {
	updatedObservation := receiptObservation(observation, state.Pending.Command, receipt)
	if state.Pending.Command.Kind == CommandUpload {
		if state.Capacity.Probe {
			state.Capacity = CapacityEvidence{}
		}
		state.Capacity.Shortfalls = 0
	}
	state.Revision++
	state.Pending.Phase = PhaseApplied
	state.Pending.Receipt = receiptSummary(receipt)
	if err := s.store.save(ctx, state); err != nil {
		return state, observation, err
	}
	resolved, err := resolvePending(state, observation, s.clock.Now())
	if err != nil {
		return state, observation, err
	}
	resolved.Revision++
	if err := s.store.save(ctx, resolved); err != nil {
		return resolved, observation, err
	}
	return resolved, updatedObservation, nil
}

func (s *service) handleDefiniteRejection(
	ctx context.Context,
	state State,
	observation samsung.Observation,
	receipt samsung.Receipt,
	applyErr error,
) (State, samsung.Observation, error) {
	state.Revision++
	commandKind := state.Pending.Command.Kind
	previous := state.Capacity
	if previous.Probe && commandKind == CommandUpload {
		state.Capacity = CapacityEvidence{}
	}
	state.Pending = nil
	storageFull := commandKind == CommandUpload && receipt.Outcome == samsung.OutcomeNotApplied &&
		errors.Is(applyErr, samsung.ErrStorageFull)
	if storageFull {
		state.Capacity = storageFullCapacity(previous, len(observation.Inventory.ContentIDs), s.clock.Now())
	}
	if err := s.store.save(context.WithoutCancel(ctx), state); err != nil {
		return state, observation, errors.Join(applyErr, err)
	}
	if storageFull {
		return state, observation, &storageFullRejectionError{cause: applyErr}
	}
	if applyErr == nil {
		applyErr = errors.New("samsung command was not applied")
	}
	if safeToRetryMutation(receipt, applyErr) {
		applyErr = &mutationRetryError{cause: applyErr}
	}
	return state, observation, applyErr
}
