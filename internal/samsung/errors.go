// Package samsung provides a client for Samsung Frame TV art management.
package samsung

import (
	"errors"
	"fmt"
	"time"
)

var (
	// errUnauthorized is returned when the TV rejects the connection
	// (e.g. token is invalid or user denied access on the TV screen).
	errUnauthorized = errors.New("samsung: TV rejected authorization")

	// errTimeout is returned when a connection or API request exceeds
	// the configured timeout.
	errTimeout = errors.New("samsung: operation timed out")

	// errNotConnected is returned when an operation is attempted on a
	// connection that has not been opened or has been closed.
	errNotConnected = errors.New("samsung: not connected")

	// errGateFailed is returned when the Silent REST Gate indicates the
	// TV is not in art mode (busy with an app or powered off).
	errGateFailed = errors.New("samsung: REST gate check failed — TV not in art mode")

	// errConnectionFailure is returned for unexpected handshake failures.
	errConnectionFailure = errors.New("samsung: connection handshake failed")

	// ErrArtAPIError is returned when the TV's art endpoint responds with an
	// application-level error code (e.g. invalid category or bad payload).
	ErrArtAPIError = errors.New("samsung: art api returned an error")

	// ErrStorageFull is returned when the TV rejects an upload because its
	// internal storage partition has no remaining space.
	ErrStorageFull = errors.New("samsung: TV storage is full")

	// errNotAuthorized is returned before any protocol write when an adapter
	// cannot prove that a command is safe.
	errNotAuthorized = errors.New("samsung: command not authorized")
)

// ErrorKind classifies Samsung failures for policy and health reporting.
type ErrorKind uint8

const (
	errorKindNone ErrorKind = iota
	ErrorKindCanceled
	ErrorKindBackoff
	ErrorKindUnreachable
	ErrorKindTimeout
	ErrorKindUnauthorized
	ErrorKindProtocol
	errorKindUnsupported
	ErrorKindInvalidResponse
	ErrorKindStorageFull
	errorKindNotAuthorized
	errorKindPersistence
	ErrorKindOutcomeUnknown
)

// String returns the stable operator-facing name used in structured logs.
func (kind ErrorKind) String() string {
	names := [...]string{
		errorKindNone:            "none",
		ErrorKindCanceled:        "canceled",
		ErrorKindBackoff:         "backoff",
		ErrorKindUnreachable:     "unreachable",
		ErrorKindTimeout:         "timeout",
		ErrorKindUnauthorized:    "unauthorized",
		ErrorKindProtocol:        "protocol",
		errorKindUnsupported:     "unsupported",
		ErrorKindInvalidResponse: "invalid_response",
		ErrorKindStorageFull:     "storage_full",
		errorKindNotAuthorized:   "not_authorized",
		errorKindPersistence:     "persistence",
		ErrorKindOutcomeUnknown:  "outcome_unknown",
	}
	if int(kind) < len(names) {
		return names[kind]
	}
	return fmt.Sprintf("unknown(%d)", kind)
}

// Outcome records whether a command definitely reached and changed the TV.
type Outcome uint8

const (
	OutcomeNotAttempted Outcome = iota
	OutcomeNotApplied
	OutcomeApplied
	OutcomeUnknown
)

// String returns the stable operator-facing name used in structured logs.
func (outcome Outcome) String() string {
	names := [...]string{
		OutcomeNotAttempted: "not_attempted",
		OutcomeNotApplied:   "not_applied",
		OutcomeApplied:      "applied",
		OutcomeUnknown:      "unknown",
	}
	if int(outcome) < len(names) {
		return names[outcome]
	}
	return fmt.Sprintf("unknown(%d)", outcome)
}

// Error preserves a stable policy classification and its original cause.
type Error struct {
	Kind                ErrorKind
	Operation           string
	RequestID           string
	Code                int
	Retryable           bool
	Outcome             Outcome
	RetryAt             time.Time
	ConsecutiveFailures int
	Cause               error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return fmt.Sprintf("samsung %s failed", e.Operation)
	}
	return fmt.Sprintf("samsung %s failed: %v", e.Operation, e.Cause)
}

// Unwrap retains context and legacy sentinel compatibility.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
