package samsung

import "time"

// ShouldSkip returns true if the TV is in a backoff window due to failures.
//
// Returns:
//   - bool: True if the client is still waiting out a failure timeout period.
func (c *Client) ShouldSkip() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Now().Before(c.backoffUntil) {
		remaining := time.Until(c.backoffUntil).Round(time.Second)
		c.logger.Info(
			"TV in backoff period, skipping",
			"failures", c.failures,
			"retry_in", remaining.String(),
		)
		return true
	}
	return false
}

// RecordFailure tracks a connection failure and calculates exponential backoff.
//
// Parameters:
//   - baseInterval: The initial time duration to wait before the next retry.
func (c *Client) RecordFailure(baseInterval time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failures++
	c.lastFailure = time.Now()

	// Exponential backoff: baseInterval doubled per consecutive failure, capped.
	delay := baseInterval
	for i := 1; i < c.failures && delay < maxBackoffDelay; i++ {
		delay *= 2
	}
	if delay > maxBackoffDelay {
		delay = maxBackoffDelay
	}

	c.backoffUntil = c.lastFailure.Add(delay)

	c.logger.Warn(
		"TV unreachable, backing off",
		"consecutive_failures", c.failures,
		"next_retry", c.backoffUntil.Format(time.Kitchen),
		"backoff_duration", delay.Round(time.Second).String(),
	)
}

// RecordSuccess resets failure count.
//
// Calling this method clears the backoff window completely.
func (c *Client) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.failures > 0 {
		c.logger.Info(
			"TV recovered after failures",
			"previous_failures", c.failures,
		)
	}
	c.failures = 0
	c.backoffUntil = time.Time{}
}
