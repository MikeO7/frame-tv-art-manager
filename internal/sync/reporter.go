package sync

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// summaryBoxWidth is the number of runes between the box's side borders.
const summaryBoxWidth = 50

// cycleSummary holds the per-cycle metrics rendered by logCycleSummary,
// bundling the many report inputs into a single cohesive value.
type cycleSummary struct {
	CycleNum  int
	StartTime time.Time
	// NextSync is when the loop's timer starts the next cycle.
	NextSync time.Time
	// Location is the configured LOCATION_TIMEZONE, the clock every schedule
	// uses, so the summary's times read the same as the operator's settings.
	Location    *time.Location
	TotalLocal  int
	FromSources int
	Optimized   int
	FailedTVs   int
	TVs         []tvSyncResult
	Warnings    []string
}

// logCycleSummary renders and logs a formatted sync cycle summary box to structured logs.
func logCycleSummary(logger *slog.Logger, s cycleSummary) {
	elapsed := time.Since(s.StartTime).Round(time.Millisecond)
	nextSync := s.NextSync.In(s.Location)

	sections := [][]string{{fmt.Sprintf("  Sync Cycle #%d - %s", s.CycleNum, time.Now().In(s.Location).Format("2006-01-02 15:04:05 MST"))}}
	for _, tv := range s.TVs {
		sections = append(sections, tvSummaryLines(tv, s.Location))
	}
	local := fmt.Sprintf("  Local:  %d files", s.TotalLocal)
	if s.FromSources > 0 {
		local += fmt.Sprintf(" │ %d from sources", s.FromSources)
	}
	if s.Optimized > 0 {
		local += fmt.Sprintf(" │ %d optimized", s.Optimized)
	}
	sections = append(sections, []string{local})
	if len(s.Warnings) > 0 {
		warnings := make([]string, 0, 1+len(s.Warnings))
		warnings = append(warnings, "  ⚠ Warnings during this cycle:")
		for _, warning := range s.Warnings {
			warnings = append(warnings, "  - "+truncateSummary(warning, 44))
		}
		sections = append(sections, warnings)
	}
	sections = append(sections, []string{"  Took:   " + elapsed.String(), "  Next:   " + nextSync.Format("15:04:05")})

	logger.Info(renderSummaryBox(sections),
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

// renderSummaryBox frames each line, truncated or padded to summaryBoxWidth
// runes, and separates consecutive sections with a divider.
func renderSummaryBox(sections [][]string) string {
	border := strings.Repeat("═", summaryBoxWidth)
	var sb strings.Builder
	sb.WriteString("\n╔" + border + "╗\n")
	for index, section := range sections {
		if index > 0 {
			sb.WriteString("╠" + border + "╣\n")
		}
		for _, line := range section {
			runes := []rune(line)
			if len(runes) > summaryBoxWidth {
				runes = runes[:summaryBoxWidth]
			}
			sb.WriteString("║" + string(runes) + strings.Repeat(" ", summaryBoxWidth-len(runes)) + "║\n")
		}
	}
	sb.WriteString("╚" + border + "╝\n")
	return sb.String()
}

func tvSummaryLines(tv tvSyncResult, location *time.Location) []string {
	name := tv.IP
	if tv.Model != "" {
		name = fmt.Sprintf("%s (%s)", tv.IP, tv.Model)
	}
	lines := []string{"  TV: " + name}
	switch tv.Status {
	case "ok":
		lines = append(lines,
			"    Status:     ✔ Art Mode",
			fmt.Sprintf("    Uploaded:   %d new  │  Deleted: %d", tv.Uploaded, tv.Deleted),
			fmt.Sprintf("    Total:      %d images on TV", tv.TotalImages),
		)
		if tv.Rotating {
			rotation := fmt.Sprintf("    Rotation:   %d waiting", tv.ArtworkWaiting)
			if !tv.NextRotation.IsZero() {
				rotation += " │ next " + tv.NextRotation.In(location).Format("Jan 2 15:04")
			}
			lines = append(lines, rotation)
		}
		if tv.Brightness != "" {
			lines = append(lines, "    Brightness: "+tv.Brightness)
		}
		if tv.Slideshow != "" {
			lines = append(lines, "    Slideshow:  "+tv.Slideshow)
		}
	case statusBackoff:
		lines = append(lines, "    Status:     ⏸ Backing off (unreachable)")
	default:
		lines = append(lines, "    Status:     ✘ "+tv.Status)
		if tv.ErrorMessage != "" {
			lines = append(lines, "    Error:      "+truncateSummary(tv.ErrorMessage, 35))
		}
	}
	return lines
}

// truncateSummary shortens value to limit bytes, ending with an ellipsis.
func truncateSummary(value string, limit int) string {
	if len(value) > limit {
		return value[:limit-3] + "..."
	}
	return value
}
