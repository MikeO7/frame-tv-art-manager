package resilience

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

const statusHealthy = "healthy"

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestBackoff_HealthyByDefault(t *testing.T) {
	b := NewBackoff(newTestLogger())
	if b.ShouldSkip("192.168.1.1") {
		t.Error("fresh IP should not be skipped")
	}
}

func TestBackoff_SkipsAfterFailure(t *testing.T) {
	b := NewBackoff(newTestLogger())
	b.maxDelay = 10 * time.Second

	b.RecordFailure("192.168.1.1", 5*time.Minute)
	// ShouldSkip should now return true (backoff in effect).
	if !b.ShouldSkip("192.168.1.1") {
		t.Error("should skip after failure")
	}
}

func TestBackoff_RecoveryResetsState(t *testing.T) {
	b := NewBackoff(newTestLogger())
	b.maxDelay = 10 * time.Second

	b.RecordFailure("192.168.1.1", 5*time.Minute)
	b.RecordSuccess("192.168.1.1")

	// After recovery, state is cleared — no more backoff.
	if _, ok := b.states["192.168.1.1"]; ok {
		t.Error("state should be removed after success")
	}
}

func TestBackoff_ExponentialGrowth(t *testing.T) {
	b := NewBackoff(newTestLogger())
	base := time.Second

	// Each failure should double the delay.
	for i := 1; i <= 4; i++ {
		b.RecordFailure("192.168.1.1", base)
		state := b.states["192.168.1.1"]
		expectedMinDelay := base * time.Duration(1<<(i-1))
		if state.failures != i {
			t.Errorf("failure %d: got failures=%d", i, state.failures)
		}
		remaining := time.Until(state.backoffUntil)
		if remaining < expectedMinDelay/2 {
			t.Errorf("failure %d: backoff %v shorter than expected min %v", i, remaining, expectedMinDelay/2)
		}
	}
}

func TestBackoff_MaxDelayEnforced(t *testing.T) {
	b := NewBackoff(newTestLogger())
	b.maxDelay = 100 * time.Millisecond

	// Fire many failures — delay should never exceed maxDelay.
	for i := 0; i < 20; i++ {
		b.RecordFailure("192.168.1.1", time.Second)
	}

	state := b.states["192.168.1.1"]
	if time.Until(state.backoffUntil) > b.maxDelay+10*time.Millisecond {
		t.Error("backoff should be capped at maxDelay")
	}
}

func TestBackoff_StatusString(t *testing.T) {
	b := NewBackoff(newTestLogger())

	if s := b.Status("192.168.1.1"); s != statusHealthy {
		t.Errorf("expected 'healthy', got %q", s)
	}

	b.RecordFailure("192.168.1.1", time.Minute)
	s := b.Status("192.168.1.1")
	if s == statusHealthy {
		t.Error("status should not be healthy after failure")
	}
}
