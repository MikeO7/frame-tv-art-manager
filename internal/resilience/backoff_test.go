package resilience

import (
	"log/slog"
	"testing"
	"time"
)

const statusHealthy = "healthy"

func TestBackoff(t *testing.T) {
	b := NewBackoff(slog.Default())
	ip := "1.1.1.1"

	if b.ShouldSkip(ip) {
		t.Error("expected ShouldSkip to be false initially")
	}

	if b.Status(ip) != statusHealthy {
		t.Errorf("expected healthy, got %s", b.Status(ip))
	}

	// Record failure
	b.RecordFailure(ip, 100*time.Millisecond)
	if !b.ShouldSkip(ip) {
		t.Error("expected ShouldSkip to be true after failure")
	}

	status := b.Status(ip)
	if status == statusHealthy {
		t.Error("expected non-healthy status after failure")
	}

	// Record success
	b.RecordSuccess(ip)
	if b.ShouldSkip(ip) {
		t.Error("expected ShouldSkip to be false after success")
	}

	// Test Status during retry (after backoff elapsed)
	b.RecordFailure(ip, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if b.ShouldSkip(ip) {
		t.Error("expected ShouldSkip to be false after backoff elapsed")
	}
	if b.Status(ip) != "retrying (1 previous failures)" {
		t.Errorf("unexpected status: %s", b.Status(ip))
	}
}

func TestBackoff_Exponential(t *testing.T) {
	b := NewBackoff(slog.Default())
	b.maxDelay = 10 * time.Millisecond
	ip := "1.1.1.1"

	b.RecordFailure(ip, 1*time.Millisecond) // 1ms
	b.RecordFailure(ip, 1*time.Millisecond) // 2ms
	b.RecordFailure(ip, 1*time.Millisecond) // 4ms
	b.RecordFailure(ip, 1*time.Millisecond) // 8ms
	b.RecordFailure(ip, 1*time.Millisecond) // 10ms (capped)
}
