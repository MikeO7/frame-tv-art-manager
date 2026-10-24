package reconcile

import (
	"slices"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func (s *service) buildPlan(
	snapshot collection.Snapshot,
	observation samsung.Observation,
	state State,
	policy Policy,
) (Plan, State, error) {
	plan, err := planSnapshot(snapshot, observation, state, policy)
	if err != nil {
		return Plan{}, state, err
	}
	if state.Capacity.Probe {
		projected := projectCapacityProbe(state, observation, state.Capacity.ObservedAt)
		probePlan, probeErr := planSnapshot(snapshot, observation, projected, policy)
		return probePlan, projected, probeErr
	}
	now := s.clock.Now()
	if !capacityProbeEligible(state.Capacity, now, s.capacityTTL) || hasCapacityMutation(plan.Commands) {
		return plan, state, nil
	}
	projected := projectCapacityProbe(state, observation, now)
	probePlan, err := planSnapshot(snapshot, observation, projected, policy)
	if err != nil {
		return Plan{}, state, err
	}
	if !hasCommand(probePlan.Commands, CommandUpload) {
		return plan, state, nil
	}
	return probePlan, projected, nil
}

func projectCapacityProbe(state State, observation samsung.Observation, observedAt time.Time) State {
	projected := cloneState(state)
	projected.Capacity = CapacityEvidence{
		Known: true, Maximum: len(observation.Inventory.ContentIDs) + 1,
		ObservedAt: observedAt, Probe: true, Shortfalls: state.Capacity.Shortfalls,
	}
	return projected
}

func capacityProbeEligible(evidence CapacityEvidence, now time.Time, ttl time.Duration) bool {
	return evidence.Known && !evidence.ObservedAt.IsZero() &&
		!now.Before(evidence.ObservedAt.Add(ttl))
}

func hasCapacityMutation(commands []CommandIntent) bool {
	return slices.ContainsFunc(commands, func(command CommandIntent) bool {
		return command.Kind == CommandUpload || command.Kind == CommandDeleteOwned || command.Kind == CommandDeleteUnknown
	})
}

func hasCommand(commands []CommandIntent, kind CommandKind) bool {
	return slices.ContainsFunc(commands, func(command CommandIntent) bool { return command.Kind == kind })
}
