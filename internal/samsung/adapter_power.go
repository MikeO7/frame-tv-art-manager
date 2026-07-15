package samsung

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type operationalState struct {
	Power   PowerState
	ArtMode ArtModeState
}

type artModeObserver interface {
	ArtMode(context.Context) (string, error)
}

func readOperationalState(
	ctx context.Context,
	transport artModeObserver,
	device DeviceInfo,
) (operationalState, error) {
	rawPower := strings.TrimSpace(device.PowerState)
	power, known := parseDevicePowerState(rawPower)
	if !known {
		return operationalState{}, fmt.Errorf("unrecognized device power state %q", device.PowerState)
	}
	if rawPower == stringOff {
		return operationalState{Power: PowerStateOff}, nil
	}
	artMode, err := transport.ArtMode(ctx)
	if err != nil {
		return operationalState{}, fmt.Errorf("read art mode: %w", err)
	}
	switch strings.TrimSpace(artMode) {
	case stringOn:
		return operationalState{Power: PowerStateOn, ArtMode: ArtModeOn}, nil
	case stringOff:
		if rawPower == stringStandby {
			power = PowerStateOff
		}
		return operationalState{Power: power, ArtMode: ArtModeOff}, nil
	default:
		return operationalState{}, fmt.Errorf("unrecognized art mode %q", artMode)
	}
}

func (a *adapter) powerOutcome(ctx context.Context, transport mutationTransport, desired string) (Outcome, error) {
	pollCtx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	lastErr := errors.New("power state has not reached the requested value")
	for {
		reached, err := desiredPowerObserved(pollCtx, transport, desired)
		if reached {
			return OutcomeApplied, nil
		}
		if err != nil {
			lastErr = err
		}
		if err := waitForPowerPoll(pollCtx, min(100*time.Millisecond, a.config.RequestTimeout)); err != nil {
			return OutcomeUnknown, powerPollError(ctx, pollCtx, lastErr)
		}
	}
}

func desiredPowerObserved(ctx context.Context, transport mutationTransport, desired string) (bool, error) {
	device, err := transport.DeviceInfo(ctx)
	if err != nil {
		return false, err
	}
	if err := validateFrameTVDevice(device); err != nil {
		return false, err
	}
	actual, err := readOperationalState(ctx, transport, device)
	if err != nil {
		return false, err
	}
	switch desired {
	case stringOn:
		return actual.Power == PowerStateOn, nil
	case stringOff:
		return actual.Power == PowerStateOff, nil
	default:
		return false, fmt.Errorf("unrecognized desired power state %q", desired)
	}
}

func waitForPowerPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func powerPollError(callerCtx, pollCtx context.Context, lastErr error) error {
	if err := callerCtx.Err(); err != nil {
		return fmt.Errorf("poll power-state postcondition: %w", err)
	}
	return fmt.Errorf("poll power-state postcondition: %w", errors.Join(pollCtx.Err(), lastErr))
}
