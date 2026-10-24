package samsung

import (
	"errors"
	"strings"
	"testing"
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

func TestSamsungErrorNilSafetyAndWrapping(t *testing.T) {
	var nilError *Error
	if nilError.Error() != "<nil>" || nilError.Unwrap() != nil {
		t.Fatal("nil Samsung error methods are not safe")
	}

	operation := operationObserve
	withoutCause := &Error{Operation: operation}
	if got, want := withoutCause.Error(), "samsung "+operation+" failed"; got != want {
		t.Fatalf("Error() = %q", got)
	}
	cause := errors.New("wire failure")
	withCause := &Error{Operation: "apply", Cause: cause}
	if !strings.Contains(withCause.Error(), cause.Error()) || !errors.Is(withCause, cause) {
		t.Fatalf("Samsung error did not preserve cause: %v", withCause)
	}
}

func TestCheckArtErrorClassifiesResponses(t *testing.T) {
	tests := []struct {
		name string
		code int
		want error
	}{
		{name: "success", code: 0},
		{name: "forbidden response", code: 403, want: ErrArtAPIError},
		{name: "insufficient storage", code: 507, want: ErrStorageFull},
		{name: "Samsung storage code", code: 11001, want: ErrStorageFull},
		{name: "generic API error", code: 42, want: ErrArtAPIError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkArtError(&artResponse{ErrorCode: tt.code})
			if tt.want == nil && err != nil {
				t.Fatalf("checkArtError: %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("checkArtError() = %v, want %v", err, tt.want)
			}
		})
	}
}
