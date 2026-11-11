package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	headerAuthorization = "Authorization"

	// defaultRateLimitBackoff applies when a provider reports exhaustion
	// without saying when its window resets.
	defaultRateLimitBackoff = time.Hour
	// maxRateLimitBackoff bounds a provider-supplied reset so a malformed
	// header cannot disable a source indefinitely.
	maxRateLimitBackoff = 24 * time.Hour
	// unixResetThreshold separates absolute Unix-time reset headers (Pexels)
	// from relative seconds-until-reset headers (Pixabay).
	unixResetThreshold = 1_000_000_000
)

// rateLimitError reports that a provider asked for no further requests until
// RetryAt. It is returned without contacting the provider while the gate is
// closed.
type rateLimitError struct {
	Provider string
	RetryAt  time.Time
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("%s rate limit reached; retry after %s", e.Provider, e.RetryAt.UTC().Format(time.RFC3339))
}

// providerError marks a failure of a provider or of the network path to it:
// an unreachable host, a non-success HTTP status, or a response that cannot be
// decoded. Only these failures, and rate limits, are worth retrying on a
// schedule; a source line or configuration the operator has to correct is not.
type providerError struct {
	message string
	cause   error
}

func (e *providerError) Error() string { return e.message }

func (e *providerError) Unwrap() error { return e.cause }

// isProviderFailure reports whether err is a provider, network, or rate-limit
// failure rather than a configuration or source-format error.
func isProviderFailure(err error) bool {
	var failure *providerError
	var limited *rateLimitError
	return errors.As(err, &failure) || errors.As(err, &limited)
}

// rateGate remembers a provider's rate-limit window across requests and
// source lines so that an exhausted quota stops further requests. A gate with
// an interval also spaces requests at least that far apart, for providers
// whose resolutions take many requests.
type rateGate struct {
	mu       sync.Mutex
	until    time.Time
	next     time.Time
	interval time.Duration
	now      func() time.Time
}

func newRateGate() *rateGate {
	return &rateGate{now: time.Now}
}

func newPacedRateGate(interval time.Duration) *rateGate {
	return &rateGate{now: time.Now, interval: interval}
}

// wait reserves the next request slot and blocks until it arrives or ctx ends.
func (g *rateGate) wait(ctx context.Context) error {
	if g == nil || g.interval <= 0 {
		return nil
	}
	g.mu.Lock()
	now := g.now()
	slot := g.next
	if slot.Before(now) {
		slot = now
	}
	g.next = slot.Add(g.interval)
	g.mu.Unlock()
	delay := slot.Sub(now)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for request slot: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (g *rateGate) check(provider string) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.now().Before(g.until) {
		return &rateLimitError{Provider: provider, RetryAt: g.until}
	}
	return nil
}

// observe closes the gate when a response reports an exhausted quota and
// returns the reset time it recorded.
func (g *rateGate) observe(header http.Header, status int) (time.Time, bool) {
	if g == nil {
		return time.Time{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	retryAt, limited := rateLimitRetryAt(header, status, g.now())
	if limited && retryAt.After(g.until) {
		g.until = retryAt
	}
	return retryAt, limited
}

// rateLimitRetryAt interprets the common provider rate-limit headers. A 429,
// or any response that reports zero remaining requests, closes the window
// until Retry-After, X-Ratelimit-Reset, or a conservative default.
func rateLimitRetryAt(header http.Header, status int, now time.Time) (time.Time, bool) {
	exhausted := strings.TrimSpace(header.Get("X-Ratelimit-Remaining")) == "0"
	if status != http.StatusTooManyRequests && !exhausted {
		return time.Time{}, false
	}
	retryAt, ok := parseRetryAfter(header.Get("Retry-After"), now)
	if !ok {
		retryAt, ok = parseRateLimitReset(header.Get("X-Ratelimit-Reset"), now)
	}
	if !ok || !retryAt.After(now) {
		retryAt = now.Add(defaultRateLimitBackoff)
	}
	if latest := now.Add(maxRateLimitBackoff); retryAt.After(latest) {
		retryAt = latest
	}
	return retryAt, true
}

func parseRetryAfter(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return now.Add(time.Duration(min(seconds, int64(maxRateLimitBackoff/time.Second))) * time.Second), true
	}
	when, err := http.ParseTime(value)
	return when, err == nil
}

func parseRateLimitReset(value string, now time.Time) (time.Time, bool) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	if seconds >= unixResetThreshold {
		return time.Unix(seconds, 0), true
	}
	return now.Add(time.Duration(min(seconds, int64(maxRateLimitBackoff/time.Second))) * time.Second), true
}

type providerJSONRequest struct {
	client      *http.Client
	gate        *rateGate
	provider    string
	url         string
	headers     map[string]string
	maxBytes    int64
	statusLabel string
	decodeLabel string
}

func fetchProviderJSON(ctx context.Context, request providerJSONRequest, out any) error {
	if err := request.gate.check(request.provider); err != nil {
		return err
	}
	if err := request.gate.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.url, nil)
	if err != nil {
		return providerRequestError(ctx, "create "+request.statusLabel, err)
	}
	req.Header.Set("User-Agent", userAgent)
	for name, value := range request.headers {
		req.Header.Set(name, value)
	}

	resp, err := request.client.Do(req)
	if err != nil {
		return providerRequestError(ctx, "request "+request.statusLabel, err)
	}
	defer func() { _ = resp.Body.Close() }()

	retryAt, limited := request.gate.observe(resp.Header, resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		if limited {
			return &rateLimitError{Provider: request.provider, RetryAt: retryAt}
		}
		return &providerError{message: fmt.Sprintf("%s error: %d", request.statusLabel, resp.StatusCode)}
	}

	reader := http.MaxBytesReader(nil, resp.Body, request.maxBytes)
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		return &providerError{message: fmt.Sprintf("decode %s response: %v", request.decodeLabel, err), cause: err}
	}
	return nil
}

func providerRequestError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return &providerError{message: fmt.Sprintf("%s failed (network timeout=%t)", operation, networkErr.Timeout())}
	}
	return &providerError{message: operation + " failed"}
}
