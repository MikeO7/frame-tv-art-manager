package samsung

import (
	"context"
	"errors"
	"fmt"
	"time"
)

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
	actual, _ := parseDevicePowerState(device.PowerState)
	switch desired {
	case stringOn:
		return actual == PowerStateOn, nil
	case stringOff:
		return actual == PowerStateOff, nil
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
