package samsung

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
)

func adapterCloseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (a *adapter) failObservation(
	ctx context.Context,
	observation Observation,
	request ObserveRequest,
	operation string,
	cause error,
) (Observation, error) {
	typed := classifyObservationError(operation, cause)
	observation.Disposition = DispositionUnsafeUnknown
	observation.ArtMode = ArtModeUnknown
	observation.Authorization = Authorization{}
	if typed.Kind == ErrorKindCanceled {
		a.publishAuthorizationFacts(observation, request)
		return cloneObservation(observation), typed
	}
	if typed.Kind == ErrorKindUnauthorized {
		observation.Connection = ConnectionAuthRequired
	} else if typed.Retryable {
		a.stateMu.Lock()
		a.runtime.ConsecutiveFailures++
		a.runtime.NextAttemptAt = a.clock.Now().Add(a.backoffDelay(a.runtime.ConsecutiveFailures))
		a.stateMu.Unlock()
	}
	if closeErr := a.transport.Close(context.WithoutCancel(ctx)); closeErr != nil {
		a.logger.Debug("close failed after observation error", "error", closeErr)
	}
	a.stateMu.Lock()
	a.connectionGeneration++
	a.stateMu.Unlock()
	a.publishAuthorizationFacts(observation, request)
	return cloneObservation(observation), typed
}

func classifyObservationError(operation string, cause error) *Error {
	kind := ErrorKindInvalidResponse
	retryable := true
	switch {
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		kind = ErrorKindCanceled
		retryable = false
	case errors.Is(cause, ErrUnauthorized):
		kind = ErrorKindUnauthorized
		retryable = false
	case errors.Is(cause, ErrTimeout):
		kind = ErrorKindTimeout
	case errors.Is(cause, ErrConnectionFailure), errors.Is(cause, ErrNotConnected):
		kind = ErrorKindUnreachable
	}
	var typed *Error
	if errors.As(cause, &typed) {
		kind = typed.Kind
		retryable = typed.Retryable
	}
	return &Error{
		Kind:      kind,
		Operation: operation,
		Retryable: retryable,
		Outcome:   OutcomeNotAttempted,
		Cause:     cause,
	}
}

func (a *adapter) backoffDelay(failures int) time.Duration {
	limit := a.config.BackoffBase
	for failure := 1; failure < failures && limit < a.config.BackoffMaximum; failure++ {
		if limit > a.config.BackoffMaximum/2 {
			limit = a.config.BackoffMaximum
			break
		}
		limit *= 2
	}
	if limit > a.config.BackoffMaximum {
		limit = a.config.BackoffMaximum
	}
	var randomBytes [8]byte
	if _, err := io.ReadFull(a.random, randomBytes[:]); err != nil {
		a.logger.Debug("backoff randomness unavailable; using maximum", "error", err)
		return limit
	}
	value := binary.BigEndian.Uint64(randomBytes[:])
	if value == math.MaxUint64 {
		return limit
	}
	return time.Duration((float64(value) / float64(math.MaxUint64)) * float64(limit))
}

func (a *adapter) resetFailures() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.runtime.ConsecutiveFailures = 0
	a.runtime.NextAttemptAt = time.Time{}
}

func (a *adapter) publishAuthorizationFacts(observation Observation, request ObserveRequest) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.runtime.InventoryFingerprint = observation.Inventory.Fingerprint
	a.runtime.CycleID = request.CycleID
	a.runtime.CollectionGeneration = request.CollectionGeneration
}

func normalizeInventory(raw json.RawMessage, observedAt time.Time) (Inventory, error) {
	content, err := parseUploadedContent(raw)
	if err != nil {
		return Inventory{}, err
	}
	ids := make([]string, 0, len(content))
	for _, item := range content {
		ids = append(ids, strings.TrimSpace(item.ContentID))
	}
	slices.Sort(ids)
	canonical, err := json.Marshal(ids)
	if err != nil {
		return Inventory{}, fmt.Errorf("encode canonical inventory: %w", err)
	}
	return Inventory{
		CategoryID:  userArtCategory,
		ContentIDs:  ids,
		Fingerprint: sha256.Sum256(canonical),
		Known:       true,
		ObservedAt:  observedAt,
	}, nil
}

func cloneObservation(observation Observation) Observation {
	observation.Inventory.ContentIDs = slices.Clone(observation.Inventory.ContentIDs)
	return observation
}
