// Package samsung provides a client for Samsung Frame TV art management.
//
// This file implements errors used across the samsung package.
package samsung

import "errors"

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
)
