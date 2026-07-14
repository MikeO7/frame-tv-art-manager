package reconcile

import (
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
	plan, err := BuildPlan(snapshot, observation, state, policy)
	if err != nil {
		return Plan{}, state, err
	}
	if state.Capacity.Probe {
		projected := projectCapacityProbe(state, observation, state.Capacity.ObservedAt)
		probePlan, probeErr := BuildPlan(snapshot, observation, projected, policy)
		return probePlan, projected, probeErr
	}
	now := s.clock.Now()
	if !capacityProbeEligible(state.Capacity, now, s.capacityTTL) || hasCapacityMutation(plan.Commands) {
		return plan, state, nil
	}
	projected := projectCapacityProbe(state, observation, now)
	probePlan, err := BuildPlan(snapshot, observation, projected, policy)
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
		ObservedAt: observedAt, Probe: true,
	}
	return projected
}

func capacityProbeEligible(evidence CapacityEvidence, now time.Time, ttl time.Duration) bool {
	return evidence.Known && !evidence.ObservedAt.IsZero() &&
		!now.Before(evidence.ObservedAt.Add(ttl))
}

func hasCapacityMutation(commands []CommandIntent) bool {
	return hasCommand(commands, CommandUpload) || hasCommand(commands, CommandDeleteOwned) ||
		hasCommand(commands, CommandDeleteUnknown)
}

func hasCommand(commands []CommandIntent, kind CommandKind) bool {
	for _, command := range commands {
		if command.Kind == kind {
			return true
		}
	}
	return false
}
