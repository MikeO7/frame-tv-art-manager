// Package resilience provides connection reliability utilities for
// handling unreachable TVs gracefully across sync cycles.
package resilience

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Backoff tracks per-TV failure state and implements exponential backoff
// to avoid hammering unreachable TVs every sync cycle.
//
// Usage:
//
//	b := NewBackoff(logger)
//	if b.ShouldSkip("192.168.1.100") { return }  // check before connecting
//	err := connectToTV(...)
//	if err != nil { b.RecordFailure("192.168.1.100") }
//	else { b.RecordSuccess("192.168.1.100") }
type Backoff struct {
	mu       sync.Mutex
	states   map[string]*tvState
	logger   *slog.Logger
	maxDelay time.Duration
}

type tvState struct {
	failures     int
	lastFailure  time.Time
	backoffUntil time.Time
}

// NewBackoff creates a backoff tracker with a default max delay of 1 hour.
func NewBackoff(logger *slog.Logger) *Backoff {
	return &Backoff{
		states:   make(map[string]*tvState),
		logger:   logger,
		maxDelay: 1 * time.Hour,
	}
}

// ShouldSkip returns true if the TV is currently in a backoff period
// due to repeated failures. Returns false if the TV has never failed
// or the backoff period has elapsed.
func (b *Backoff) ShouldSkip(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[ip]
	if !ok {
		return false
	}

	if time.Now().Before(state.backoffUntil) {
		remaining := time.Until(state.backoffUntil).Round(time.Second)
		b.logger.Info("TV in backoff period, skipping",
			"tv", ip,
			"failures", state.failures,
			"retry_in", remaining.String(),
		)
		return true
	}

	return false
}

// RecordFailure increments the failure count for a TV and calculates
// the next backoff period using exponential backoff (2^failures * base,
// capped at maxDelay).
func (b *Backoff) RecordFailure(ip string, baseInterval time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[ip]
	if !ok {
		state = &tvState{}
		b.states[ip] = state
	}

	state.failures++
	state.lastFailure = time.Now()

	// Exponential backoff: 2^failures * base interval, capped.
	delay := baseInterval
	for i := 1; i < state.failures && delay < b.maxDelay; i++ {
		delay *= 2
	}
	if delay > b.maxDelay {
		delay = b.maxDelay
	}

	state.backoffUntil = time.Now().Add(delay)

	b.logger.Warn("TV unreachable, backing off",
		"tv", ip,
		"consecutive_failures", state.failures,
		"next_retry", state.backoffUntil.Format(time.Kitchen),
		"backoff_duration", delay.Round(time.Second).String(),
	)
}

// RecordSuccess resets the failure state for a TV. If the TV was
// previously in backoff, logs a recovery message.
func (b *Backoff) RecordSuccess(ip string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[ip]
	if !ok {
		return
	}

	if state.failures > 0 {
		b.logger.Info("TV recovered after failures",
			"tv", ip,
			"previous_failures", state.failures,
		)
	}

	delete(b.states, ip)
}

// Status returns a human-readable status string for a TV.
func (b *Backoff) Status(ip string) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[ip]
	if !ok {
		return "healthy"
	}

	if time.Now().Before(state.backoffUntil) {
		return fmt.Sprintf("backoff (%d failures, retry in %s)",
			state.failures,
			time.Until(state.backoffUntil).Round(time.Second),
		)
	}

	return fmt.Sprintf("retrying (%d previous failures)", state.failures)
}
