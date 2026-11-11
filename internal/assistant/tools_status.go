package assistant

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

type syncStatus struct {
	Lifecycle  string `json:"lifecycle"`
	Stage      string `json:"stage"`
	Uptime     string `json:"uptime"`
	SyncCount  int    `json:"sync_count"`
	LastSync   string `json:"last_sync,omitempty"`
	LastSyncOK bool   `json:"last_sync_ok"`
	LastError  string `json:"last_error,omitempty"`
	// SourceWarning is why the last artwork source sync failed.
	SourceWarning string `json:"source_warning,omitempty"`
	IntervalMins  int    `json:"sync_interval_minutes"`
	DryRun        bool   `json:"dry_run"`
}

type themedStatus struct {
	ActiveToday   []string `json:"active_today"`
	EverydayShown bool     `json:"everyday_shown"`
	Selected      int      `json:"selected"`
	Total         int      `json:"total"`
	FileError     string   `json:"collections_file_error,omitempty"`
}

type statusOutput struct {
	Sync   syncStatus        `json:"sync"`
	TVs    []health.TVStatus `json:"tvs"`
	Themed themedStatus      `json:"themed_collections"`
}

type syncNowOutput struct {
	Queued  bool   `json:"queued" jsonschema:"false when a requested cycle was already waiting"`
	Message string `json:"message"`
}

func (svc *service) registerStatusTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:  "get_status",
		Title: "Get sync and TV status",
		Description: "Reports the sync loop (last cycle, errors, current stage), each TV's state (reachable, Art Mode, " +
			"image count, rotation), and what today's themed collection rules select.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPointer(false)},
	}, svc.getStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "sync_now",
		Title: "Sync the TVs now",
		Description: "Starts a sync cycle as soon as the current one (if any) finishes, instead of waiting for the " +
			"next interval. Repeated calls collapse into one cycle. Use get_status to follow the result.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)},
	}, svc.syncNow)
}

func (svc *service) getStatus(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ emptyInput,
) (*mcp.CallToolResult, statusOutput, error) {
	report := svc.status.Report()
	output := statusOutput{
		Sync: syncStatus{
			Lifecycle: report.Lifecycle, Stage: report.CurrentStage,
			Uptime:    svc.now().Sub(report.StartedAt).Round(time.Second).String(),
			SyncCount: report.SyncCount, LastSyncOK: report.LastSyncOK, LastError: report.LastError,
			SourceWarning: report.SourceWarning,
			IntervalMins:  svc.cfg.SyncIntervalMin, DryRun: svc.cfg.DryRun,
		},
		TVs: report.TVs,
	}
	if !report.LastSyncAt.IsZero() {
		output.Sync.LastSync = report.LastSyncAt.UTC().Format(time.RFC3339)
	}
	themed, err := svc.themedStatus(ctx)
	if err != nil {
		return nil, statusOutput{}, err
	}
	output.Themed = themed
	return nil, output, nil
}

// themedStatus summarizes today's selection. An unreadable collections file
// is reported in the status rather than failing it, because the status is how
// the operator finds out.
func (svc *service) themedStatus(ctx context.Context) (themedStatus, error) {
	file, loadErr := svc.themes.Load(ctx)
	if loadErr != nil {
		return themedStatus{ActiveToday: []string{}, EverydayShown: true, FileError: loadErr.Error()}, ctx.Err()
	}
	snapshot, err := svc.inventory(ctx)
	if err != nil {
		return themedStatus{}, err
	}
	selection := themes.Select(file, snapshot.Items, svc.today())
	return themedStatus{
		ActiveToday: selection.Active, EverydayShown: !selection.Exclusive,
		Selected: len(selection.Items), Total: len(snapshot.Items),
	}, nil
}

func (svc *service) syncNow(
	_ context.Context,
	_ *mcp.CallToolRequest,
	_ emptyInput,
) (*mcp.CallToolResult, syncNowOutput, error) {
	queued := svc.artwork.RequestSync()
	message := "A sync cycle will start as soon as the current one finishes."
	if !queued {
		message = "A sync cycle was already requested; it will include these changes."
	}
	if svc.cfg.DryRun {
		message += " DRY_RUN is on, so the cycle only logs what it would change."
	}
	svc.logger.Info("sync requested over MCP", "queued", queued)
	return nil, syncNowOutput{Queued: queued, Message: message}, nil
}
