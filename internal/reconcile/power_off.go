package reconcile

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// powerOffAfterFailure keeps a scheduled power-off independent of artwork and
// settings work: when an earlier command failed, the TV is observed afresh
// and still turned off. It never acts on unknown state. A pending command
// with an unknown outcome, or a dry run, leaves the failure as it is.
func (s *service) powerOffAfterFailure(
	ctx context.Context,
	request Request,
	result Result,
	runErr error,
) (Result, error) {
	policy := normalizePolicy(request.Policy)
	if policy.Power != PowerOff || request.DryRun || request.TV == nil ||
		result.powerOffAttempted || slices.Contains(result.AppliedCommands, commandPowerOff) ||
		result.State.Version == 0 || result.State.Pending != nil || errors.Is(runErr, ErrPersistenceUnknown) {
		return result, runErr
	}
	observation, err := request.TV.Observe(ctx, samsung.ObserveRequest{
		CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
		Required: requiredCapabilities(commandIntent{Kind: commandPowerOff}),
	})
	if err != nil {
		return result, errors.Join(runErr, fmt.Errorf("observe TV for scheduled power-off: %w", err))
	}
	if observation.Disposition != samsung.DispositionEligible {
		// Off, or not showing art: the guarded power-off has nothing to do.
		return result, runErr
	}
	identity, err := identityFromObservation(observation)
	if err != nil || !sameStableTV(identity, result.State.TV) {
		return result, errors.Join(runErr, errors.New("scheduled power-off observed a different TV"))
	}
	state, observation, err := s.execute(ctx, executionInput{
		request: request, policy: policy, state: result.State, observation: observation,
		intent: commandIntent{Kind: commandPowerOff},
	})
	result.State, result.Observation = cloneState(state), observation
	if err != nil {
		return result, errors.Join(runErr, fmt.Errorf("scheduled power-off: %w", err))
	}
	s.logger.Warn("scheduled power-off ran after an earlier command failed",
		"cycle_id", request.CycleID, "error", runErr)
	result.Applied++
	result.AppliedCommands = append(result.AppliedCommands, commandPowerOff)
	return result, runErr
}
