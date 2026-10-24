package samsung

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestClassifyObservationErrorPolicy(t *testing.T) {
	operation := operationObserve
	tests := []struct {
		name      string
		cause     error
		wantKind  ErrorKind
		retryable bool
	}{
		{name: "canceled", cause: context.Canceled, wantKind: ErrorKindCanceled},
		{name: "deadline", cause: context.DeadlineExceeded, wantKind: ErrorKindCanceled},
		{name: "unauthorized", cause: ErrUnauthorized, wantKind: ErrorKindUnauthorized},
		{name: "timeout", cause: ErrTimeout, wantKind: ErrorKindTimeout, retryable: true},
		{name: "connection failure", cause: ErrConnectionFailure, wantKind: ErrorKindUnreachable, retryable: true},
		{name: "not connected", cause: ErrNotConnected, wantKind: ErrorKindUnreachable, retryable: true},
		{name: "invalid response", cause: errors.New("malformed response"), wantKind: ErrorKindInvalidResponse, retryable: true},
		{
			name: "typed policy is preserved",
			cause: &Error{
				Kind:      ErrorKindPersistence,
				Operation: "persist token",
				Retryable: false,
				Cause:     io.ErrUnexpectedEOF,
			},
			wantKind: ErrorKindPersistence,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyObservationError(operation, test.cause)
			if got.Kind != test.wantKind || got.Retryable != test.retryable {
				t.Fatalf("classifyObservationError() = %+v, want kind %v retryable %t", got, test.wantKind, test.retryable)
			}
			if got.Operation != operation || got.Outcome != OutcomeNotAttempted || !errors.Is(got, test.cause) {
				t.Fatalf("classification lost operation, outcome, or cause: %+v", got)
			}
		})
	}
}

func TestBackoffDelayIsBoundedAndHandlesEntropyFailure(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		random   io.Reader
		want     time.Duration
	}{
		{name: "minimum jitter", failures: 1, random: bytes.NewReader(make([]byte, 8)), want: 0},
		{name: "maximum jitter", failures: 1, random: bytes.NewReader(bytes.Repeat([]byte{0xff}, 8)), want: 10 * time.Second},
		{name: "exponential maximum", failures: 20, random: bytes.NewReader(bytes.Repeat([]byte{0xff}, 8)), want: 40 * time.Second},
		{name: "entropy failure fails to upper bound", failures: 2, random: bytes.NewReader(nil), want: 20 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &adapter{
				config: Config{BackoffBase: 10 * time.Second, BackoffMaximum: 40 * time.Second},
				random: test.random,
				logger: slog.New(slog.DiscardHandler),
			}
			if got := adapter.backoffDelay(test.failures); got != test.want {
				t.Fatalf("backoffDelay(%d) = %v, want %v", test.failures, got, test.want)
			}
		})
	}
}

func TestAdapterCloseContextRetainsCleanupOpportunity(t *testing.T) {
	active := context.Background()
	activeClose, activeCancel := adapterCloseContext(active, time.Second)
	defer activeCancel()
	if activeClose != active {
		t.Fatal("active context was unnecessarily replaced")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cleanup, cleanupCancel := adapterCloseContext(canceled, time.Second)
	defer cleanupCancel()
	if cleanup.Err() != nil {
		t.Fatalf("cleanup context inherited cancellation: %v", cleanup.Err())
	}
	if _, ok := cleanup.Deadline(); !ok {
		t.Fatal("cleanup context has no bounded deadline")
	}
}

func TestObservationBackoffBlocksNetworkAndReportsSchedule(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)}
	transport := &scriptedObservationTransport{connectErr: ErrConnectionFailure}
	adapter := newTestAdapter(t, clock, transport)

	_, firstErr := adapter.Observe(context.Background(), ObserveRequest{
		CycleID: "cycle-1", CollectionGeneration: "generation",
	})
	var first *Error
	if !errors.As(firstErr, &first) {
		t.Fatalf("first Observe() error = %v, want typed Samsung error", firstErr)
	}
	wantRetryAt := clock.now.Add(time.Minute)
	if first.Kind != ErrorKindUnreachable || first.ConsecutiveFailures != 1 || !first.RetryAt.Equal(wantRetryAt) {
		t.Fatalf("first error = %+v, want failure 1 retry at %s", first, wantRetryAt)
	}

	observation, secondErr := adapter.Observe(context.Background(), ObserveRequest{
		CycleID: "cycle-2", CollectionGeneration: "generation",
	})
	if observation.Disposition != DispositionBlockedBackoff || observation.Connection != ConnectionBackingOff ||
		!unauthorized(observation.Authorization) {
		t.Fatalf("backoff observation = %+v, want blocked without authorization", observation)
	}
	var second *Error
	if !errors.As(secondErr, &second) {
		t.Fatalf("second Observe() error = %v, want typed Samsung error", secondErr)
	}
	if second.Kind != ErrorKindBackoff || second.ConsecutiveFailures != 1 || !second.RetryAt.Equal(wantRetryAt) {
		t.Fatalf("backoff error = %+v, want failure 1 retry at %s", second, wantRetryAt)
	}
	if transport.connectCalls != 1 {
		t.Fatalf("connect calls = %d, want one while backing off", transport.connectCalls)
	}
}
