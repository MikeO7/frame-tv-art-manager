package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

//nolint:funlen,gocognit // The linear sequence is the durable reconciliation protocol.
func (s *service) run(ctx context.Context, request Request) (Result, error) {
	policy := normalizePolicy(request.Policy)
	if err := validateRequest(request.CycleID, request.TV, request.Snapshot, policy, request.DryRun); err != nil {
		return Result{}, err
	}
	policy, err := s.withoutHandledWake(request.TV.Address(), policy)
	if err != nil {
		return Result{}, err
	}
	observation, err := request.TV.Observe(ctx, samsung.ObserveRequest{
		CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
		DryRun: request.DryRun, Required: initialObservationCapabilities(policy),
		Optional: settingCapabilities(policy),
	})
	if err != nil {
		return Result{Observation: observation}, err
	}
	var scheduledWake []CommandKind
	if policy.Power == PowerOn {
		step, wakeErr := s.scheduledWake(ctx, request, policy, observation)
		policy, observation, scheduledWake, err = step.policy, step.observation, step.applied, wakeErr
		if err != nil {
			return Result{
				Status: statusForError(err), Observation: observation,
				Applied: len(scheduledWake), AppliedCommands: scheduledWake,
			}, err
		}
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
		request: request, observation: observation, identity: identity,
		state: state, exists: exists,
	})
	if err != nil {
		return Result{Observation: observation}, err
	}
	state, err = s.recover(ctx, request, observation, state)
	if err != nil {
		return Result{Status: statusForError(err), State: state, Observation: observation}, err
	}
	uploadUnresolved := state.UnresolvedUpload != nil
	wake := wakeApplied(scheduledWake)
	if state.LegacyMigrationPending && observation.Disposition == samsung.DispositionEligible {
		state, err = s.completeDeferredLegacyMigration(ctx, state, request.Snapshot, observation, request.DryRun)
		if err != nil {
			return wake.result(statusForError(err), state, observation), err
		}
	}
	if observation.Disposition != samsung.DispositionEligible {
		return wake.result(StatusKnownSkip, state, observation), nil
	}
	selectedThisCycle := false
	// stalePrunes maps each digest whose binding left the TV to the content ID
	// that went missing, so a binding renewed by an upload later in the same
	// cycle is not mistaken for the stale one.
	stalePrunes := make(map[string]string)
	// replan plans the next step, never selecting artwork twice in one cycle.
	replan := func(observation samsung.Observation, state State) (Plan, State, error) {
		plan, planned, err := s.buildPlan(request.Snapshot, observation, state, policy)
		if err == nil && selectedThisCycle {
			plan.Commands = withoutCommand(plan.Commands, commandSelect)
		}
		if err == nil && uploadUnresolved {
			plan.Commands = artworkCommandsBlocked(plan.Commands)
			plan.PruneBindings = nil
		}
		for _, digest := range plan.PruneBindings {
			stalePrunes[digest] = state.Bindings[digest].ContentID
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
	result := wake.result(StatusComplete, state, observation)
	result.Plan, result.Rotation = plan, plan.Rotation
	if request.DryRun {
		result.Status = StatusIncompleteDryRun
		return result, nil
	}
	currentPlan := plan
	storageFullAbsorbed := false
	paceMutation := false
	var lastAttempt *commandIntent
	commandAttempts := 0
	for len(currentPlan.Commands) > 0 {
		candidate := currentPlan.Commands[0]
		fresh, observeErr := request.TV.Observe(ctx, samsung.ObserveRequest{
			CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
			Required: requiredCapabilities(candidate), Optional: settingCapabilities(policy),
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
		if command.Kind == commandPowerOff {
			result.powerOffAttempted = true
		}
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
		if command.Kind == commandPowerOff {
			break
		}
		if command.Kind == commandSelect {
			selectedThisCycle = true
		}
		currentPlan, state, err = replan(observation, state)
		if err != nil {
			return result, err
		}
	}
	state.Pending = nil
	result.Rotation = currentPlan.Rotation
	if uploadUnresolved {
		// Settings and power converged; artwork waits for inventory to prove
		// the parked upload, so the cycle is not complete.
		result.Status = StatusRecoveryRequired
		result.State = cloneState(state)
		return result, fmt.Errorf("upload %q outcome is unproven: %w",
			state.UnresolvedUpload.Command.Name, ErrRecoveryRequired)
	}
	if currentPlan.CapacityLimited {
		result.Status = StatusStorageFull
		result.State = cloneState(state)
		return result, samsung.ErrStorageFull
	}
	desired := snapshotDigests(request.Snapshot)
	now := s.clock.Now()
	for digest, staleContentID := range stalePrunes {
		if state.Bindings[digest].ContentID != staleContentID {
			continue
		}
		delete(state.Bindings, digest)
		if _, wanted := desired[digest]; wanted {
			recordShown(&state, digest, now)
		}
	}
	retainShownHistory(&state, desired)
	retainDisplayHistory(&state, desired)
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

// wakeApplied is the scheduled wake a run already sent, if any. Every result
// of the run reports it.
type wakeApplied []CommandKind

func (w wakeApplied) result(status Status, state State, observation samsung.Observation) Result {
	return Result{
		Status: status, State: cloneState(state), Observation: observation,
		Applied: len(w), AppliedCommands: slices.Clone(w),
	}
}

func (s *service) logMutationRetry(
	ctx context.Context,
	cycleID string,
	command commandIntent,
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

// recover settles pending work and any parked upload before planning. An
// upload whose outcome stays unproven is parked instead of stopping the run.
func (s *service) recover(
	ctx context.Context,
	request Request,
	observation samsung.Observation,
	state State,
) (State, error) {
	if state.Pending != nil {
		resolved, err := s.resolveRecovery(ctx, request.CycleID, request.DryRun, observation, state)
		if parkableUpload(resolved, err) {
			resolved, err = s.parkUpload(ctx, request.CycleID, request.DryRun, resolved)
		}
		if err != nil {
			return resolved, err
		}
		state = resolved
	}
	return s.settleUnresolvedUpload(ctx, request.CycleID, request.DryRun, observation, state)
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
