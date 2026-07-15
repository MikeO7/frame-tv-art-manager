package reconcile

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

//nolint:funlen,gocognit,gocyclo // The linear sequence is the durable reconciliation protocol.
func (s *service) run(ctx context.Context, request Request) (Result, error) {
	policy := s.effectivePolicy(request.Policy)
	if err := validateRequest(request.CycleID, request.TV, request.Snapshot, policy, request.DryRun); err != nil {
		return Result{}, err
	}
	observation, err := observe(ctx, request.TV, samsung.ObserveRequest{
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
		recovery, recoveryErr := s.resolveRecovery(ctx, recoveryInput{
			cycleID: request.CycleID, dryRun: request.DryRun, observation: observation, state: state,
		})
		if recoveryErr != nil || !recovery.Resolved {
			return Result{Status: recovery.Status, State: recovery.State, Observation: recovery.Observation}, recoveryErr
		}
		state = recovery.State
		observation = recovery.Observation
	}
	wake := wakePreparation{state: state, observation: observation}
	if policy.Power == PowerOn {
		wake, err = s.preparePowerOn(ctx, request, policy, state, observation)
		if wake.terminal != nil {
			return *wake.terminal, err
		}
		if err != nil {
			return Result{
				Status: statusForError(err), Plan: wake.plan, State: cloneState(wake.state),
				Observation: wake.observation, Applied: wake.applied,
				AppliedCommands: slices.Clone(wake.appliedCommands),
			}, err
		}
		state = wake.state
		observation = wake.observation
	}
	if state.LegacyMigrationPending {
		state, err = s.completeDeferredLegacyMigration(ctx, state, request.Snapshot, observation, request.DryRun)
		if err != nil {
			return Result{
				Status: statusForError(err), Plan: wake.plan, State: cloneState(state),
				Observation: observation, Applied: wake.applied,
				AppliedCommands: slices.Clone(wake.appliedCommands),
			}, err
		}
	}
	if observation.Disposition != samsung.DispositionEligible {
		return Result{
			Status: StatusKnownSkip, Plan: wake.plan, State: cloneState(state),
			Observation: observation, Applied: wake.applied,
			AppliedCommands: slices.Clone(wake.appliedCommands),
		}, nil
	}
	plan, plannedState, err := s.buildPlan(request.Snapshot, observation, state, policy)
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
	result := Result{
		Plan: resultPlan, State: cloneState(state), Observation: observation, Applied: wake.applied,
		AppliedCommands: slices.Clone(wake.appliedCommands),
	}
	if request.DryRun {
		result.Status = StatusIncompleteDryRun
		return result, nil
	}
	if unsupported := firstUnsupported(plan.Commands); unsupported != nil {
		result.Status = StatusUnsupported
		return result, fmt.Errorf("%w: %s", ErrUnsupportedIntent, unsupported.Kind)
	}
	currentPlan := plan
	selectedThisCycle := false
	paceMutation := false
	var lastAttempt *CommandIntent
	commandAttempts := 0
	pruneBindings := make(map[string]struct{}, len(plan.PruneBindings))
	for _, digest := range plan.PruneBindings {
		pruneBindings[digest] = struct{}{}
	}
	for len(currentPlan.Commands) > 0 {
		candidate := currentPlan.Commands[0]
		fresh, observeErr := observe(ctx, request.TV, samsung.ObserveRequest{
			CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
			Required: requiredCapabilities([]CommandIntent{candidate}),
		})
		if observeErr != nil {
			return result, observeErr
		}
		if fresh.Disposition != samsung.DispositionEligible {
			return result, errors.New("TV is no longer eligible for the planned command")
		}
		refreshedPlan, refreshedState, planErr := s.buildPlan(request.Snapshot, fresh, state, policy)
		if planErr != nil {
			return result, planErr
		}
		if selectedThisCycle {
			refreshedPlan.Commands = withoutCommand(refreshedPlan.Commands, CommandSelect)
		}
		for _, digest := range refreshedPlan.PruneBindings {
			pruneBindings[digest] = struct{}{}
		}
		if len(refreshedPlan.Commands) == 0 {
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
		if err != nil {
			if isMutationRetry(err) && commandAttempts < s.mutations.attempts {
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
		currentPlan, state, err = s.buildPlan(request.Snapshot, observation, state, policy)
		if err != nil {
			return result, err
		}
		if selectedThisCycle {
			currentPlan.Commands = withoutCommand(currentPlan.Commands, CommandSelect)
		}
	}
	state.Pending = nil
	if currentPlan.CapacityLimited {
		result.Status = StatusStorageFull
		result.State = cloneState(state)
		return result, samsung.ErrStorageFull
	}
	for digest := range pruneBindings {
		delete(state.Bindings, digest)
	}
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

type recoveryInput struct {
	cycleID     string
	dryRun      bool
	observation samsung.Observation
	state       State
}

type recoveryResult struct {
	Status      Status
	State       State
	Observation samsung.Observation
	Resolved    bool
}

func (s *service) resolveRecovery(ctx context.Context, input recoveryInput) (recoveryResult, error) {
	dryRun, observation, state := input.dryRun, input.observation, input.state
	if state.Pending == nil {
		return recoveryResult{Status: dryRunStatus(dryRun), State: cloneState(state), Observation: observation, Resolved: true}, nil
	}
	resolved, changed, err := resolvePending(state, observation, s.clock.Now())
	if err != nil {
		return recoveryResult{Status: StatusRecoveryRequired, State: cloneState(state), Observation: observation}, err
	}
	if !changed {
		return recoveryResult{Status: StatusRecoveryRequired, State: cloneState(state), Observation: observation}, ErrRecoveryRequired
	}
	if dryRun {
		return recoveryResult{Status: StatusIncompleteDryRun, State: resolved, Observation: observation, Resolved: true}, nil
	}
	resolved.Revision++
	if err := s.store.save(ctx, resolved); err != nil {
		return recoveryResult{Status: statusForError(err), State: resolved, Observation: observation}, err
	}
	s.logger.Info("reconciliation pending operation resolved", "cycle_id", input.cycleID,
		"operation_id", state.Pending.OperationID, "phase", state.Pending.Phase)
	return recoveryResult{Status: StatusComplete, State: resolved, Observation: observation, Resolved: true}, nil
}
