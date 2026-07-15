// Package samsung provides a client for Samsung Frame TV art management.
//
// This file implements errors used across the samsung package.
package samsung

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrUnauthorized is returned when the TV rejects the connection
	// (e.g. token is invalid or user denied access on the TV screen).
	ErrUnauthorized = errors.New("samsung: TV rejected authorization")

	// ErrTimeout is returned when a connection or API request exceeds
	// the configured timeout.
	ErrTimeout = errors.New("samsung: operation timed out")

	// ErrNotConnected is returned when an operation is attempted on a
	// connection that has not been opened or has been closed.
	ErrNotConnected = errors.New("samsung: not connected")

	// ErrGateFailed is returned when the Silent REST Gate indicates the
	// TV is not in art mode (busy with an app or powered off).
	ErrGateFailed = errors.New("samsung: REST gate check failed — TV not in art mode")

	// ErrConnectionFailure is returned for unexpected handshake failures.
	ErrConnectionFailure = errors.New("samsung: connection handshake failed")

	// ErrArtAPIError is returned when the TV's art endpoint responds with an
	// application-level error code (e.g. invalid category or bad payload).
	ErrArtAPIError = errors.New("samsung: art api returned an error")

	// ErrStorageFull is returned when the TV rejects an upload because its
	// internal storage partition has no remaining space.
	ErrStorageFull = errors.New("samsung: TV storage is full")

	// ErrNotAuthorized is returned before any protocol write when an adapter
	// cannot prove that a command is safe.
	ErrNotAuthorized = errors.New("samsung: command not authorized")
)

// ErrorKind classifies Samsung failures for policy and health reporting.
type ErrorKind uint8

const (
	ErrorKindNone ErrorKind = iota
	ErrorKindCanceled
	ErrorKindBackoff
	ErrorKindUnreachable
	ErrorKindTimeout
	ErrorKindUnauthorized
	ErrorKindProtocol
	ErrorKindUnsupported
	ErrorKindInvalidResponse
	ErrorKindStorageFull
	ErrorKindNotAuthorized
	ErrorKindPersistence
	ErrorKindOutcomeUnknown
)

// String returns the stable operator-facing name used in structured logs.
//
//nolint:gocyclo,goconst // An exhaustive enum mapping is clearer and safer than a positional name table.
func (kind ErrorKind) String() string {
	switch kind {
	case ErrorKindNone:
		return "none"
	case ErrorKindCanceled:
		return "canceled"
	case ErrorKindBackoff:
		return "backoff"
	case ErrorKindUnreachable:
		return "unreachable"
	case ErrorKindTimeout:
		return "timeout"
	case ErrorKindUnauthorized:
		return "unauthorized"
	case ErrorKindProtocol:
		return "protocol"
	case ErrorKindUnsupported:
		return "unsupported"
	case ErrorKindInvalidResponse:
		return "invalid_response"
	case ErrorKindStorageFull:
		return "storage_full"
	case ErrorKindNotAuthorized:
		return "not_authorized"
	case ErrorKindPersistence:
		return "persistence"
	case ErrorKindOutcomeUnknown:
		return "outcome_unknown"
	default:
		return fmt.Sprintf("unknown(%d)", kind)
	}
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
//
//nolint:goconst // These are stable enum names, not interchangeable domain constants.
func (outcome Outcome) String() string {
	switch outcome {
	case OutcomeNotAttempted:
		return "not_attempted"
	case OutcomeNotApplied:
		return "not_applied"
	case OutcomeApplied:
		return "applied"
	case OutcomeUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("unknown(%d)", outcome)
	}
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
