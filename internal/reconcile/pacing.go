package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const (
	maximumMutationPacing   = 5 * time.Minute
	maximumMutationAttempts = 10
	defaultCapacityTTL      = 24 * time.Hour
	maximumCapacityTTL      = 365 * 24 * time.Hour
)

// Option configures stateful reconciliation execution without expanding the
// pure planning policy.
type Option interface {
	configure(*runtimeConfiguration) error
}

type optionFunc func(*runtimeConfiguration) error

func (option optionFunc) configure(configuration *runtimeConfiguration) error {
	return option(configuration)
}

// WithMutationPacing configures the context-cancellable delay between
// consecutive mutation attempts. Zero disables pacing.
func WithMutationPacing(delay time.Duration) Option {
	return optionFunc(func(configuration *runtimeConfiguration) error {
		if delay < 0 || delay > maximumMutationPacing {
			return fmt.Errorf("mutation pacing must be between 0 and %s", maximumMutationPacing)
		}
		configuration.mutations.delay = delay
		return nil
	})
}

// WithMutationAttempts configures the maximum number of attempts for a
// command that is unequivocally reported as not attempted. One disables retry.
func WithMutationAttempts(attempts int) Option {
	return optionFunc(func(configuration *runtimeConfiguration) error {
		if attempts < 1 || attempts > maximumMutationAttempts {
			return fmt.Errorf("mutation attempts must be between 1 and %d", maximumMutationAttempts)
		}
		configuration.mutations.attempts = attempts
		return nil
	})
}

// WithCapacityEvidenceTTL configures how long a storage-full observation
// suppresses cautious upload probes.
func WithCapacityEvidenceTTL(ttl time.Duration) Option {
	return optionFunc(func(configuration *runtimeConfiguration) error {
		if ttl <= 0 || ttl > maximumCapacityTTL {
			return fmt.Errorf("capacity evidence TTL must be between 1ns and %s", maximumCapacityTTL)
		}
		configuration.capacityTTL = ttl
		return nil
	})
}

type mutationWaiter interface {
	Wait(context.Context, time.Duration) error
}

type timerMutationWaiter struct{}

func (timerMutationWaiter) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type mutationExecution struct {
	delay    time.Duration
	attempts int
	waiter   mutationWaiter
}

type runtimeConfiguration struct {
	mutations   mutationExecution
	capacityTTL time.Duration
}

func newRuntimeConfiguration(options []Option) (runtimeConfiguration, error) {
	configuration := runtimeConfiguration{
		mutations:   mutationExecution{attempts: 1, waiter: timerMutationWaiter{}},
		capacityTTL: defaultCapacityTTL,
	}
	for index, option := range options {
		if option == nil {
			return runtimeConfiguration{}, fmt.Errorf("reconciliation option %d is nil", index)
		}
		if err := option.configure(&configuration); err != nil {
			return runtimeConfiguration{}, fmt.Errorf("configure reconciliation option %d: %w", index, err)
		}
	}
	if configuration.mutations.waiter == nil {
		return runtimeConfiguration{}, errors.New("mutation waiter is required")
	}
	return configuration, nil
}

func (execution mutationExecution) wait(ctx context.Context, pace bool) error {
	if !pace || execution.delay == 0 {
		return nil
	}
	return execution.waiter.Wait(ctx, execution.delay)
}

// mutationRetryError can only be created after the adapter unequivocally
// reports that it did not attempt the command and that cleared pending state
// has been durably persisted.
type mutationRetryError struct {
	cause error
}

func (err *mutationRetryError) Error() string { return err.cause.Error() }
func (err *mutationRetryError) Unwrap() error { return err.cause }

func safeToRetryMutation(receipt samsung.Receipt, applyErr error) bool {
	if receipt.Outcome != samsung.OutcomeNotAttempted || applyErr == nil {
		return false
	}
	var protocolErr *samsung.Error
	return errors.As(applyErr, &protocolErr) && protocolErr.Retryable &&
		protocolErr.Outcome == samsung.OutcomeNotAttempted
}

func isMutationRetry(err error) bool {
	var retryErr *mutationRetryError
	return errors.As(err, &retryErr)
}

func withMutationWaiter(waiter mutationWaiter) Option {
	return optionFunc(func(configuration *runtimeConfiguration) error {
		if waiter == nil {
			return errors.New("mutation waiter is required")
		}
		configuration.mutations.waiter = waiter
		return nil
	})
}
