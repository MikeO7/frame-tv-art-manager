package samsung

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOperatorErrorNamesAreStableAndExhaustive(t *testing.T) {
	t.Parallel()

	kinds := []struct {
		value ErrorKind
		want  string
	}{
		{ErrorKindNone, "none"},
		{ErrorKindCanceled, "canceled"},
		{ErrorKindBackoff, "backoff"},
		{ErrorKindUnreachable, "unreachable"},
		{ErrorKindTimeout, "timeout"},
		{ErrorKindUnauthorized, "unauthorized"},
		{ErrorKindProtocol, "protocol"},
		{ErrorKindUnsupported, "unsupported"},
		{ErrorKindInvalidResponse, "invalid_response"},
		{ErrorKindStorageFull, "storage_full"},
		{ErrorKindNotAuthorized, "not_authorized"},
		{ErrorKindPersistence, "persistence"},
		{ErrorKindOutcomeUnknown, "outcome_unknown"},
		{ErrorKind(255), "unknown(255)"},
	}
	for _, test := range kinds {
		if got := test.value.String(); got != test.want {
			t.Errorf("ErrorKind(%d).String() = %q, want %q", test.value, got, test.want)
		}
	}

	outcomes := []struct {
		value Outcome
		want  string
	}{
		{OutcomeNotAttempted, "not_attempted"},
		{OutcomeNotApplied, "not_applied"},
		{OutcomeApplied, "applied"},
		{OutcomeUnknown, "unknown"},
		{Outcome(255), "unknown(255)"},
	}
	for _, test := range outcomes {
		if got := test.value.String(); got != test.want {
			t.Errorf("Outcome(%d).String() = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestObservationErrorsReportBackoffSchedule(t *testing.T) {
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

	_, secondErr := adapter.Observe(context.Background(), ObserveRequest{
		CycleID: "cycle-2", CollectionGeneration: "generation",
	})
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
