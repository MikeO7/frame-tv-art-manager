package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// slowArtworkCollection takes delay to prepare every cycle.
type slowArtworkCollection struct {
	artworkCollection
	delay time.Duration
}

func (collection slowArtworkCollection) prepareCycle(ctx context.Context) (preparedCollection, error) {
	select {
	case <-time.After(collection.delay):
	case <-ctx.Done():
		return preparedCollection{}, ctx.Err()
	}
	return collection.artworkCollection.prepareCycle(ctx)
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *lockedBuffer) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(data)
}

// nextSyncTimes returns next_sync_at of every cycle summary logged so far.
func (output *lockedBuffer) nextSyncTimes(t *testing.T) []time.Time {
	t.Helper()
	output.mu.Lock()
	defer output.mu.Unlock()
	var times []time.Time
	for line := range bytes.Lines(output.buffer.Bytes()) {
		var entry struct {
			Event      string    `json:"event"`
			NextSyncAt time.Time `json:"next_sync_at"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if entry.Event == "sync_cycle_summary" {
			times = append(times, entry.NextSyncAt)
		}
	}
	return times
}

func TestCycleSummaryReportsTheTimersNextSync(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const cycleTakes = 10 * time.Minute
		fixture := newThemedEngineFixture(t, "UTC")
		interval := time.Duration(fixture.engine.cfg.SyncIntervalMin) * time.Minute
		output := &lockedBuffer{}
		fixture.engine.logger = slog.New(slog.NewJSONHandler(output, nil))
		fixture.engine.collection = slowArtworkCollection{artworkCollection: fixture.engine.collection, delay: cycleTakes}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		started := time.Now()
		go func() { done <- fixture.engine.RunLoop(ctx) }()

		// The startup cycle ends when the timer starts, one interval before
		// the first timed cycle.
		timerStarted := started.Add(cycleTakes)
		time.Sleep(cycleTakes + time.Minute)
		synctest.Wait()
		// A timed cycle that takes ten minutes leaves fifty until the next.
		time.Sleep(interval + cycleTakes)
		synctest.Wait()
		// A requested cycle is extra and does not move the timer.
		if !fixture.engine.requestCycle() {
			t.Fatal("requestCycle() was not queued")
		}
		time.Sleep(cycleTakes + time.Minute)
		synctest.Wait()

		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("RunLoop() error = %v, want cancellation", err)
		}
		want := []time.Time{
			timerStarted.Add(interval),
			timerStarted.Add(2 * interval),
			timerStarted.Add(2 * interval),
		}
		got := output.nextSyncTimes(t)
		if len(got) != len(want) {
			t.Fatalf("logged %d cycle summaries (%v), want %d", len(got), got, len(want))
		}
		for index := range want {
			// The cycle's own short waits move the timer's start by
			// milliseconds; the errors guarded against are minutes.
			if got[index].Sub(want[index]).Abs() > time.Second {
				t.Errorf("cycle %d next_sync_at = %v, want %v", index+1, got[index], want[index])
			}
		}
	})
}

func TestNextSyncAt(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	interval := 30 * time.Minute
	fixture := newThemedEngineFixture(t, "UTC")
	fixture.engine.cfg.SyncIntervalMin = 30

	now := started.Add(5 * time.Minute)
	if got := fixture.engine.nextSyncAt(now); !got.Equal(now.Add(interval)) {
		t.Fatalf("nextSyncAt before the timer starts = %v, want one interval from now", got)
	}
	tests := []struct {
		name string
		tick time.Duration
		now  time.Duration
		want time.Duration
	}{
		{name: "tick on schedule", tick: 30 * time.Minute, now: 31 * time.Minute, want: 60 * time.Minute},
		{name: "tick delivered late stays on schedule", tick: 47 * time.Minute, now: 50 * time.Minute, want: 60 * time.Minute},
		{name: "skipped ticks", tick: 95 * time.Minute, now: 96 * time.Minute, want: 120 * time.Minute},
		{name: "cycle outlasted the interval", tick: 30 * time.Minute, now: 70 * time.Minute, want: 70 * time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture.engine.setNextTick(tickAfter(started, interval, started.Add(test.tick)))
			if got := fixture.engine.nextSyncAt(started.Add(test.now)); !got.Equal(started.Add(test.want)) {
				t.Fatalf("nextSyncAt = %v, want %v", got, started.Add(test.want))
			}
		})
	}
}

func TestCycleSummaryFallsBackToUTCForAnUnloadableTimezone(t *testing.T) {
	t.Parallel()
	fixture := newThemedEngineFixture(t, "Not/A_Zone")
	var output bytes.Buffer
	if got := fixture.engine.summaryLocation(slog.New(slog.NewJSONHandler(&output, nil))); got != time.UTC {
		t.Fatalf("summaryLocation() = %v, want UTC", got)
	}
	if entry := decodeStructuredLog(t, output.Bytes()); entry["level"] != "WARN" {
		t.Fatalf("log = %v, want a warning that UTC is used", entry)
	}
	fixture.engine.cfg.Timezone = "America/Denver"
	if got := fixture.engine.summaryLocation(slog.Default()); got.String() != "America/Denver" {
		t.Fatalf("summaryLocation() = %v, want America/Denver", got)
	}
}
