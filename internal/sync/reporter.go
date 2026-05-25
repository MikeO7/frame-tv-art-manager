package sync

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LogCycleSummary renders and logs a formatted sync cycle summary box to structured logs.
//
//nolint:gocognit,gocyclo,revive,funlen // complexity justified for this domain-specific path
func LogCycleSummary(
	logger *slog.Logger,
	cycleNum int,
	startTime time.Time,
	syncIntervalMin int,
	totalLocal int,
	fromSources int,
	optimized int,
	tvs []TVSyncResult,
	warnings []string,
) {
	elapsed := time.Since(startTime).Round(time.Millisecond)
	nextSync := time.Now().Add(time.Duration(syncIntervalMin) * time.Minute)

	const boxWidth = 50

	padLine := func(content string) string {
		runes := []rune(content)
		if len(runes) > boxWidth {
			runes = runes[:boxWidth]
		}
		padding := boxWidth - len(runes)
		return "║" + string(runes) + strings.Repeat(" ", padding) + "║\n"
	}

	var sb strings.Builder
	sb.WriteString("\n╔══════════════════════════════════════════════════╗\n")

	header := fmt.Sprintf("  Sync Cycle #%d - %s", cycleNum, time.Now().Format("2006-01-02 15:04:05"))
	sb.WriteString(padLine(header))

	sb.WriteString("╠══════════════════════════════════════════════════╣\n")

	for _, tv := range tvs {
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
		sb.WriteString("╠══════════════════════════════════════════════════╣\n")
	}

	localSummary := fmt.Sprintf("  Local:  %d files", totalLocal)
	if fromSources > 0 {
		localSummary += fmt.Sprintf(" │ %d from URLs", fromSources)
	}
	if optimized > 0 {
		localSummary += fmt.Sprintf(" │ %d optimized", optimized)
	}
	sb.WriteString(padLine(localSummary))

	if len(warnings) > 0 {
		sb.WriteString("╠══════════════════════════════════════════════════╣\n")
		sb.WriteString(padLine("  ⚠ Warnings during this cycle:"))
		for _, w := range warnings {
			if len(w) > 44 {
				w = w[:41] + "..."
			}
			sb.WriteString(padLine("  - " + w))
		}
	}

	sb.WriteString("╠══════════════════════════════════════════════════╣\n")
	sb.WriteString(padLine("  Took:   " + elapsed.String()))
	sb.WriteString(padLine("  Next:   " + nextSync.Format("15:04:05")))
	sb.WriteString("╚══════════════════════════════════════════════════╝\n")

	logger.Info(sb.String())
}
