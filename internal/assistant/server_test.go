package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

func TestInitializeAdvertisesWorkflowAndTools(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	initialized := fixture.session.InitializeResult()
	if initialized == nil || initialized.ServerInfo == nil || initialized.ServerInfo.Name != serverName ||
		!strings.Contains(initialized.Instructions, "get_display_spec") || !strings.Contains(initialized.Instructions, "sync_now") {
		t.Fatalf("initialize result = %+v, want server identity and workflow instructions", initialized)
	}
	listed, err := fixture.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema == nil {
			t.Fatalf("tool %s lacks a description or input schema", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{
		"add_artwork", "create_collection", "delete_collection", "get_display_spec", "get_status", "list_artwork",
		"list_collections", "preview_artwork", "remove_from_collection", "sync_now", "update_collection",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestGetDisplaySpecReportsTheTargetAndCurrentFitting(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{environ: []string{"IMAGE_MAX_CROP_PERCENT=10", "IMAGE_PAD_STYLE=blur"}})
	var spec displaySpec
	fixture.call(t, "get_display_spec", nil, &spec)
	if spec.Width != 3840 || spec.Height != 2160 || spec.AspectRatio != "16:9" || spec.ColorSpace != "sRGB" ||
		!slices.Equal(spec.Formats, []string{"JPEG", "PNG"}) || spec.MaxImageBytes != 2<<20 {
		t.Fatalf("display spec = %+v", spec)
	}
	if spec.Fitting.MaxCropPercent != 10 || spec.Fitting.PadStyle != "blur" ||
		!strings.Contains(spec.Fitting.Summary, "at most 10%") || !strings.Contains(spec.Fitting.Summary, "blurred") {
		t.Fatalf("fitting = %+v, want the configured crop limit and pad style", spec.Fitting)
	}
	tips := strings.Join(spec.PromptTips, " ")
	for _, want := range []string{"16:9", "Painterly", "pure-white", "watermarks"} {
		if !strings.Contains(tips, want) {
			t.Fatalf("prompt tips %q do not mention %q", tips, want)
		}
	}
}

func TestCollectionLifecycle(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	var created collectionChangeOutput
	fixture.call(t, "create_collection", map[string]any{"name": "Dutch Winter", "description": "Skaters"}, &created)
	if created.Collection.Slug != "dutch-winter" || created.Collection.Activation.Mode != "off" ||
		!created.Collection.Exclusive || created.Collection.ActiveToday {
		t.Fatalf("created = %+v, want an off, exclusive collection", created)
	}
	if text := fixture.callError(t, "create_collection", map[string]any{"name": "dutch winter"}); !strings.Contains(text, "already exists") {
		t.Fatalf("duplicate create error = %q", text)
	}

	var updated collectionChangeOutput
	fixture.call(t, "update_collection", map[string]any{
		"collection": "Dutch Winter", "exclusive": false,
		"activation": map[string]any{"mode": "window", "start": "12-01", "end": "01-06"},
	}, &updated)
	if updated.Collection.Exclusive || !updated.Collection.ActiveToday || updated.Collection.Schedule != "shown every year from 12-01 through 01-06" {
		t.Fatalf("updated = %+v, want a non-exclusive window active on Christmas Eve", updated)
	}
	for name, arguments := range map[string]map[string]any{
		"bad date":     {"collection": "dutch-winter", "activation": map[string]any{"mode": "window", "start": "12-32", "end": "01-06"}},
		"bad mode":     {"collection": "dutch-winter", "activation": map[string]any{"mode": "weekly"}},
		"missing":      {"collection": "Easter", "exclusive": true},
		"nothing to do": {"collection": "dutch-winter"},
	} {
		if text := fixture.callError(t, "update_collection", arguments); text == "" {
			t.Fatalf("%s: update_collection error has no message", name)
		}
	}

	var listed listCollectionsOutput
	fixture.call(t, "list_collections", nil, &listed)
	if listed.Today != "2026-12-24" || listed.Timezone != "UTC" || len(listed.Collections) != 1 ||
		!slices.Equal(listed.ActiveToday, []string{"dutch-winter"}) || !listed.EverydayShown {
		t.Fatalf("list_collections = %+v", listed)
	}

	var deleted removeOutput
	fixture.call(t, "delete_collection", map[string]any{"collection": "dutch-winter"}, &deleted)
	if deleted.Collection != "dutch-winter" || !strings.Contains(deleted.Message, "No image files were deleted") {
		t.Fatalf("delete_collection = %+v", deleted)
	}
	if text := fixture.callError(t, "delete_collection", map[string]any{"collection": "dutch-winter"}); !strings.Contains(text, "not found") {
		t.Fatalf("second delete error = %q", text)
	}
}

func TestCreateCollectionWithScheduleAndInclusiveFlag(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	var created collectionChangeOutput
	fixture.call(t, "create_collection", map[string]any{
		"name": "Summer", "exclusive": false, "activation": map[string]any{"mode": "ALWAYS"},
	}, &created)
	if created.Collection.Exclusive || created.Collection.Activation.Mode != "always" || !created.Collection.ActiveToday {
		t.Fatalf("created = %+v", created)
	}
	if text := fixture.callError(t, "create_collection", map[string]any{"name": "!!!"}); !strings.Contains(text, "letter or digit") {
		t.Fatalf("invalid name error = %q", text)
	}
}

func TestStatusAndSyncNow(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	fixture.status.SetLifecycle("ready")
	fixture.status.SetTVStatus("192.0.2.10", health.TVStatus{IP: "192.0.2.10", Status: "ok", ArtMode: true, ImageCount: 4})
	fixture.status.RecordSync(false, errors.New("tv 192.0.2.10: unreachable"))

	var status statusOutput
	fixture.call(t, "get_status", nil, &status)
	if status.Sync.Lifecycle != "ready" || status.Sync.SyncCount != 1 || status.Sync.LastSyncOK ||
		!strings.Contains(status.Sync.LastError, "unreachable") || len(status.TVs) != 1 || !status.TVs[0].ArtMode ||
		!status.Themed.EverydayShown || status.Sync.IntervalMins != 30 {
		t.Fatalf("get_status = %+v", status)
	}

	var first, second syncNowOutput
	fixture.call(t, "sync_now", nil, &first)
	fixture.call(t, "sync_now", nil, &second)
	if !first.Queued || second.Queued || !strings.Contains(second.Message, "already requested") {
		t.Fatalf("sync_now = %+v then %+v, want the second request to collapse", first, second)
	}
}

func TestStatusReportsAnUnreadableCollectionsFile(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	writeCorruptCollections(t, fixture.themes)
	var status statusOutput
	fixture.call(t, "get_status", nil, &status)
	if !strings.Contains(status.Themed.FileError, "collections.json") {
		t.Fatalf("get_status themed = %+v, want the file error", status.Themed)
	}
	if text := fixture.callError(t, "list_collections", nil); !strings.Contains(text, "collections.json") {
		t.Fatalf("list_collections error = %q", text)
	}
}

func TestDryRunRefusesEveryChange(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{environ: []string{"DRY_RUN=true"}})
	for name, arguments := range map[string]map[string]any{
		"add_artwork":            {"collection": "Christmas", "image_base64": base64Of(paintedJPEG(t, 32, 18, 1))},
		"create_collection":      {"name": "Christmas"},
		"update_collection":      {"collection": "Christmas", "exclusive": true},
		"remove_from_collection": {"collection": "Christmas", "artwork": "tree.jpg"},
		"delete_collection":      {"collection": "Christmas"},
	} {
		if text := fixture.callError(t, name, arguments); !strings.Contains(text, themes.ErrDryRun.Error()) {
			t.Fatalf("%s in dry run error = %q, want the dry-run refusal", name, text)
		}
	}
	var spec displaySpec
	fixture.call(t, "get_display_spec", nil, &spec)
	if !spec.DryRun {
		t.Fatal("display spec does not report dry run")
	}
	var queued syncNowOutput
	fixture.call(t, "sync_now", nil, &queued)
	if !strings.Contains(queued.Message, "DRY_RUN") {
		t.Fatalf("sync_now in dry run = %+v", queued)
	}
}

func TestNewHandlerValidatesDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewHandler(Options{}); err == nil {
		t.Fatal("NewHandler() accepted missing dependencies")
	}
}

func TestToolsHaveSafetyAnnotations(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	listed, err := fixture.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{
		"get_display_spec": true, "preview_artwork": true, "list_artwork": true, "list_collections": true, "get_status": true,
	}
	for _, tool := range listed.Tools {
		annotations := tool.Annotations
		if annotations == nil {
			t.Fatalf("tool %s has no annotations", tool.Name)
		}
		if annotations.ReadOnlyHint != readOnly[tool.Name] {
			t.Fatalf("tool %s read-only hint = %t", tool.Name, annotations.ReadOnlyHint)
		}
		if !annotations.ReadOnlyHint && (annotations.DestructiveHint == nil || *annotations.DestructiveHint) {
			t.Fatalf("tool %s must declare that it never destroys artwork", tool.Name)
		}
	}
}
