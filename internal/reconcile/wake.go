package reconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type wakePreparation struct {
	state           State
	observation     samsung.Observation
	plan            Plan
	applied         int
	appliedCommands []CommandKind
	terminal        *Result
}

func (s *service) preparePowerOn(
	ctx context.Context,
	request Request,
	policy Policy,
	state State,
	observation samsung.Observation,
) (wakePreparation, error) {
	prepared := wakePreparation{state: state, observation: observation}
	switch observation.Power {
	case samsung.PowerStateOn:
		return s.observeForConvergence(ctx, request, policy, prepared)
	case samsung.PowerStateOff:
		// Continue below. A wake is authorized only by this positive off fact.
	default:
		return prepared, errors.New("explicit power-on policy requires a known TV power state")
	}

	if observation.Capabilities.RemotePower != samsung.SupportSupported {
		err := fmt.Errorf("%w: wake requires a configured MAC address and remote-power support", ErrUnsupportedIntent)
		result := Result{
			Status: StatusUnsupported, State: cloneState(state), Observation: observation,
		}
		prepared.terminal = &result
		return prepared, err
	}

	policyFingerprint, err := fingerprintPolicy(policy)
	if err != nil {
		return prepared, err
	}
	prepared.plan = Plan{
		CollectionGeneration: request.Snapshot.Generation,
		PolicyFingerprint:    policyFingerprint,
		InventoryFingerprint: observation.Inventory.Fingerprint,
		Commands:             []CommandIntent{{Kind: CommandWake}},
	}
	if request.DryRun {
		result := Result{
			Status: StatusIncompleteDryRun, Plan: prepared.plan, State: cloneState(state),
			Observation: observation,
		}
		prepared.terminal = &result
		return prepared, nil
	}

	prepared.state, prepared.observation, err = s.execute(ctx, executionInput{
		request: request, policy: policy, state: state, observation: observation,
		intent: CommandIntent{Kind: CommandWake},
	})
	if err != nil {
		return prepared, err
	}
	prepared.applied = 1
	prepared.appliedCommands = []CommandKind{CommandWake}
	return s.observeForConvergence(ctx, request, policy, prepared)
}

func (s *service) observeForConvergence(
	ctx context.Context,
	request Request,
	policy Policy,
	prepared wakePreparation,
) (wakePreparation, error) {
	observation, err := observe(ctx, request.TV, samsung.ObserveRequest{
		CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
		DryRun: request.DryRun, Required: convergenceObservationCapabilities(policy),
	})
	prepared.observation = observation
	return prepared, err
}
