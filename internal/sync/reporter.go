package sync

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// CycleSummary holds the per-cycle metrics rendered by LogCycleSummary,
// bundling the many report inputs into a single cohesive value.
type CycleSummary struct {
	CycleNum        int
	StartTime       time.Time
	SyncIntervalMin int
	TotalLocal      int
	FromSources     int
	Optimized       int
	FailedTVs       int
	TVs             []TVSyncResult
	Warnings        []string
}

// LogCycleSummary renders and logs a formatted sync cycle summary box to structured logs.
func LogCycleSummary(logger *slog.Logger, s CycleSummary) {
	elapsed := time.Since(s.StartTime).Round(time.Millisecond)
	nextSync := time.Now().Add(time.Duration(s.SyncIntervalMin) * time.Minute)

	const boxWidth = 50

	// Borders are derived from boxWidth so the frame stays aligned with padLine.
	border := strings.Repeat("═", boxWidth)
	topBorder := "\n╔" + border + "╗\n"
	midBorder := "╠" + border + "╣\n"
	bottomBorder := "╚" + border + "╝\n"

	padLine := func(content string) string {
		runes := []rune(content)
		if len(runes) > boxWidth {
			runes = runes[:boxWidth]
		}
		padding := boxWidth - len(runes)
		return "║" + string(runes) + strings.Repeat(" ", padding) + "║\n"
	}

	var sb strings.Builder
	sb.WriteString(topBorder)

	header := fmt.Sprintf("  Sync Cycle #%d - %s", s.CycleNum, time.Now().Format("2006-01-02 15:04:05"))
	sb.WriteString(padLine(header))

	sb.WriteString(midBorder)

	for _, tv := range s.TVs {
		writeTVSummary(&sb, tv, padLine)
		sb.WriteString(midBorder)
	}

	localSummary := fmt.Sprintf("  Local:  %d files", s.TotalLocal)
	if s.FromSources > 0 {
		localSummary += fmt.Sprintf(" │ %d from URLs", s.FromSources)
	}
	if s.Optimized > 0 {
		localSummary += fmt.Sprintf(" │ %d optimized", s.Optimized)
	}
	sb.WriteString(padLine(localSummary))

	if len(s.Warnings) > 0 {
		sb.WriteString(midBorder)
		writeWarningsSummary(&sb, s.Warnings, padLine)
	}

	sb.WriteString(midBorder)
	sb.WriteString(padLine("  Took:   " + elapsed.String()))
	sb.WriteString(padLine("  Next:   " + nextSync.Format("15:04:05")))
	sb.WriteString(bottomBorder)

	logStructuredCycleSummary(logger, sb.String(), s, elapsed, nextSync)
}

func logStructuredCycleSummary(
	logger *slog.Logger,
	message string,
	s CycleSummary,
	elapsed time.Duration,
	nextSync time.Time,
) {
	logger.Info(message,
		"event", "sync_cycle_summary",
		"cycle", s.CycleNum,
		"duration_ms", elapsed.Milliseconds(),
		"next_sync_at", nextSync,
		"local_artwork", s.TotalLocal,
		"source_downloads", s.FromSources,
		"optimized", s.Optimized,
		"tv_count", len(s.TVs),
		"failed_tvs", s.FailedTVs,
		"warnings", len(s.Warnings),
	)
}

func writeTVSummary(sb *strings.Builder, tv TVSyncResult, padLine func(string) string) {
	name := tv.IP
	if tv.Model != "" {
		name = fmt.Sprintf("%s (%s)", tv.IP, tv.Model)
	}
	sb.WriteString(padLine("  TV: " + name))

	switch tv.Status {
	case "ok":
		sb.WriteString(padLine("    Status:     ✔ Art Mode"))
		sb.WriteString(padLine(fmt.Sprintf("    Uploaded:   %d new  │  Deleted: %d", tv.Uploaded, tv.Deleted)))
		sb.WriteString(padLine(fmt.Sprintf("    Total:      %d images on TV", tv.TotalImages)))
		if tv.Brightness != "" {
			sb.WriteString(padLine("    Brightness: " + tv.Brightness))
		}
		if tv.Slideshow != "" {
			sb.WriteString(padLine("    Slideshow:  " + tv.Slideshow))
		}
	case statusBackoff:
		sb.WriteString(padLine("    Status:     ⏸ Backing off (unreachable)"))
	default:
		sb.WriteString(padLine("    Status:     ✘ " + tv.Status))
		if tv.ErrorMessage != "" {
			errMsg := tv.ErrorMessage
			if len(errMsg) > 35 {
				errMsg = errMsg[:32] + "..."
			}
			sb.WriteString(padLine("    Error:      " + errMsg))
		}
	}
}

func writeWarningsSummary(sb *strings.Builder, warnings []string, padLine func(string) string) {
	sb.WriteString(padLine("  ⚠ Warnings during this cycle:"))
	for _, w := range warnings {
		if len(w) > 44 {
			w = w[:41] + "..."
		}
		sb.WriteString(padLine("  - " + w))
	}
}
