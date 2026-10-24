package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

//nolint:funlen,gocognit // The linear sequence is the durable reconciliation protocol.
func (s *service) run(ctx context.Context, request Request) (Result, error) {
	policy := s.effectivePolicy(request.Policy)
	if err := validateRequest(request.CycleID, request.TV, request.Snapshot, policy, request.DryRun); err != nil {
		return Result{}, err
	}
	observation, err := request.TV.Observe(ctx, samsung.ObserveRequest{
		CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
		DryRun: request.DryRun, Required: initialObservationCapabilities(policy),
	})
	if err != nil {
		return Result{Observation: observation}, err
	}
	identity, err := identityFromObservation(observation)
	if err != nil {
		return Result{Observation: observation}, err
	}
	state, exists, err := s.store.load(ctx, identity)
	if err != nil {
		return Result{Observation: observation}, err
	}
	state, err = s.prepareInitialState(ctx, initialStateInput{
		request: request, policy: policy, observation: observation, identity: identity,
		state: state, exists: exists,
	})
	if err != nil {
		return Result{Observation: observation}, err
	}
	if state.Pending != nil {
		state, err = s.resolveRecovery(ctx, request.CycleID, request.DryRun, observation, state)
		if err != nil {
			return Result{Status: statusForError(err), State: state, Observation: observation}, err
		}
	}
	wake := wakePreparation{state: state, observation: observation}
	if policy.Power == PowerOn {
		wake, err = s.preparePowerOn(ctx, request, policy, state, observation)
		if wake.terminal != nil {
			return *wake.terminal, err
		}
		if err != nil {
			return wake.result(statusForError(err), wake.state, wake.observation), err
		}
		state = wake.state
		observation = wake.observation
	}
	if state.LegacyMigrationPending {
		state, err = s.completeDeferredLegacyMigration(ctx, state, request.Snapshot, observation, request.DryRun)
		if err != nil {
			return wake.result(statusForError(err), state, observation), err
		}
	}
	if observation.Disposition != samsung.DispositionEligible {
		return wake.result(StatusKnownSkip, state, observation), nil
	}
	selectedThisCycle := false
	// replan plans the next step, never selecting artwork twice in one cycle.
	replan := func(observation samsung.Observation, state State) (Plan, State, error) {
		plan, planned, err := s.buildPlan(request.Snapshot, observation, state, policy)
		if err == nil && selectedThisCycle {
			plan.Commands = withoutCommand(plan.Commands, CommandSelect)
		}
		return plan, planned, err
	}
	plan, plannedState, err := replan(observation, state)
	if err != nil {
		return Result{State: cloneState(state), Observation: observation}, err
	}
	if !request.DryRun {
		state = plannedState
	}
	resultPlan := plan
	if len(wake.plan.Commands) > 0 {
		resultPlan.Commands = append(slices.Clone(wake.plan.Commands), resultPlan.Commands...)
	}
	result := wake.result(StatusComplete, state, observation)
	result.Plan, result.Rotation = resultPlan, plan.Rotation
	if request.DryRun {
		result.Status = StatusIncompleteDryRun
		return result, nil
	}
	currentPlan := plan
	storageFullAbsorbed := false
	paceMutation := false
	var lastAttempt *CommandIntent
	commandAttempts := 0
	pruneBindings := make(map[string]struct{}, len(plan.PruneBindings))
	for _, digest := range plan.PruneBindings {
		pruneBindings[digest] = struct{}{}
	}
	for len(currentPlan.Commands) > 0 {
		candidate := currentPlan.Commands[0]
		fresh, observeErr := request.TV.Observe(ctx, samsung.ObserveRequest{
			CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
			Required: requiredCapabilities(candidate),
		})
		if observeErr != nil {
			return result, observeErr
		}
		if fresh.Disposition != samsung.DispositionEligible {
			return result, errors.New("TV is no longer eligible for the planned command")
		}
		refreshedPlan, refreshedState, planErr := replan(fresh, state)
		if planErr != nil {
			return result, planErr
		}
		for _, digest := range refreshedPlan.PruneBindings {
			pruneBindings[digest] = struct{}{}
		}
		if len(refreshedPlan.Commands) == 0 {
			currentPlan = refreshedPlan
			break
		}
		command := refreshedPlan.Commands[0]
		if !sameIntent(candidate, command) {
			state = refreshedState
			currentPlan = refreshedPlan
			continue
		}
		state = refreshedState
		observation = fresh
		result.Observation = fresh
		if lastAttempt == nil || !sameIntent(*lastAttempt, command) {
			commandAttempts = 0
		}
		attempted := command
		lastAttempt = &attempted
		commandAttempts++
		state, observation, err = s.execute(ctx, executionInput{
			request: request, policy: policy, state: state, observation: observation, intent: command,
			pace: paceMutation,
		})
		paceMutation = true
		result.State = cloneState(state)
		result.Observation = observation
		// A full TV is the expected entry into rotation. The rejection and its
		// capacity evidence are already durable, so replan once against them.
		if policy.Rotation > 0 && !storageFullAbsorbed && isStorageFullRejection(err) {
			storageFullAbsorbed = true
			lastAttempt, commandAttempts = nil, 0
			s.logger.Info("TV storage is full; rotating artwork", "cycle_id", request.CycleID,
				"capacity", state.Capacity.Maximum, "rotation_interval", policy.Rotation.String())
			currentPlan, state, err = replan(observation, state)
			if err != nil {
				return result, err
			}
			continue
		}
		if err != nil {
			if isMutationRetry(err) && commandAttempts < s.mutations.attempts {
				s.logMutationRetry(ctx, request.CycleID, command, commandAttempts, err)
				currentPlan = refreshedPlan
				continue
			}
			result.Status = statusForError(err)
			return result, err
		}
		lastAttempt = nil
		commandAttempts = 0
		result.Applied++
		result.AppliedCommands = append(result.AppliedCommands, command.Kind)
		if command.Kind == CommandPowerOff {
			break
		}
		if command.Kind == CommandSelect {
			selectedThisCycle = true
		}
		currentPlan, state, err = replan(observation, state)
		if err != nil {
			return result, err
		}
	}
	state.Pending = nil
	result.Rotation = currentPlan.Rotation
	if currentPlan.CapacityLimited {
		result.Status = StatusStorageFull
		result.State = cloneState(state)
		return result, samsung.ErrStorageFull
	}
	desired := snapshotDigests(request.Snapshot)
	now := s.clock.Now()
	for digest := range pruneBindings {
		delete(state.Bindings, digest)
		if _, wanted := desired[digest]; wanted {
			recordShown(&state, digest, now)
		}
	}
	retainShownHistory(&state, desired)
	state.LastCompleteCycle = request.CycleID
	state.LastCollectionGen = request.Snapshot.Generation
	state.Revision++
	if err := s.store.save(ctx, state); err != nil {
		result.State = cloneState(state)
		result.Status = statusForError(err)
		return result, err
	}
	result.Status = StatusComplete
	result.State = cloneState(state)
	return result, nil
}

func (s *service) logMutationRetry(
	ctx context.Context,
	cycleID string,
	command CommandIntent,
	attempt int,
	err error,
) {
	attrs := []slog.Attr{
		slog.String("cycle_id", cycleID),
		slog.String("command", string(command.Kind)),
		slog.Int("attempt", attempt),
		slog.Int("next_attempt", attempt+1),
		slog.Int("max_attempts", s.mutations.attempts),
		slog.Any("error", err),
	}
	var samsungErr *samsung.Error
	if errors.As(err, &samsungErr) {
		attrs = append(attrs,
			slog.String("error_kind", samsungErr.Kind.String()),
			slog.String("operation", samsungErr.Operation),
			slog.Bool("retryable", samsungErr.Retryable),
			slog.String("outcome", samsungErr.Outcome.String()),
		)
	}
	s.logger.LogAttrs(ctx, slog.LevelWarn, "Samsung mutation failed; retrying", attrs...)
}

// resolveRecovery settles outstanding pending work before any new planning.
// On failure it returns the state to report with the error.
func (s *service) resolveRecovery(
	ctx context.Context,
	cycleID string,
	dryRun bool,
	observation samsung.Observation,
	state State,
) (State, error) {
	resolved, err := resolvePending(state, observation, s.clock.Now())
	if err != nil {
		return cloneState(state), err
	}
	if dryRun {
		return resolved, nil
	}
	resolved.Revision++
	if err := s.store.save(ctx, resolved); err != nil {
		return resolved, err
	}
	s.logger.Info("reconciliation pending operation resolved", "cycle_id", cycleID,
		"operation_id", state.Pending.OperationID, "phase", state.Pending.Phase)
	return resolved, nil
}
