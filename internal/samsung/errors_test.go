package samsung

import (
	"encoding/json"
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
		{errorKindNone, "none"},
		{ErrorKindCanceled, "canceled"},
		{ErrorKindBackoff, "backoff"},
		{ErrorKindUnreachable, "unreachable"},
		{ErrorKindTimeout, "timeout"},
		{ErrorKindUnauthorized, "unauthorized"},
		{ErrorKindProtocol, "protocol"},
		{errorKindUnsupported, "unsupported"},
		{ErrorKindInvalidResponse, "invalid_response"},
		{ErrorKindStorageFull, "storage_full"},
		{errorKindNotAuthorized, "not_authorized"},
		{errorKindPersistence, "persistence"},
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
		name  string
		raw   string
		want  error
		match string
	}{
		{name: "success", raw: `{"event":"get_artmode_status"}`},
		{name: "explicit zero", raw: `{"error_code":0}`},
		{name: "forbidden response", raw: `{"error_code":403}`, want: ErrArtAPIError},
		{name: "insufficient storage", raw: `{"error_code":507}`, want: ErrStorageFull},
		{name: "Samsung storage code", raw: `{"error_code":11001}`, want: ErrStorageFull},
		{name: "string storage code", raw: `{"error_code":"507"}`, want: ErrStorageFull},
		{name: "real TV string code", raw: `{"event":"error","error_code":"-1"}`, want: ErrArtAPIError, match: "code -1"},
		{name: "error event without code", raw: `{"event":"error"}`, want: ErrArtAPIError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response artResponse
			if err := json.Unmarshal([]byte(tt.raw), &response); err != nil {
				t.Fatalf("decode %s: %v", tt.raw, err)
			}
			err := checkArtError(&response)
			if tt.match != "" && (err == nil || !strings.Contains(err.Error(), tt.match)) {
				t.Fatalf("checkArtError() = %v, want message containing %q", err, tt.match)
			}
			if tt.want == nil && err != nil {
				t.Fatalf("checkArtError: %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("checkArtError() = %v, want %v", err, tt.want)
			}
		})
	}
}
