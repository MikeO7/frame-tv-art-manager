// Package assistant serves the Model Context Protocol tools an AI assistant
// uses to build themed artwork collections. The manager holds no AI provider
// keys and generates nothing: the assistant creates images with its own
// tools, then calls these tools to learn the display, store the images in a
// collection, preview them, schedule the collection, and request a sync.
package assistant

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

const (
	serverName    = "frame-tv-art-manager"
	serverTitle   = "Frame TV Art Manager"
	defaultMaxMB  = 20
	requestSlack  = 1 << 20
	base64Growth  = 4.0 / 3.0
	shortDigestHx = 12
)

// instructions is the workflow the server describes to connecting assistants.
const instructions = "This server manages the artwork shown on Samsung Frame TVs. It does not generate images: " +
	"create them with your own image tools, then use these tools to put them on the TV.\n\n" +
	"Workflow:\n" +
	"1. Call get_display_spec first. Generate images to that spec: landscape 16:9 at the largest size your " +
	"image model supports, JPEG or PNG. To use real paintings instead, call search_public_domain_art (when " +
	"offered) and keep the candidates you like.\n" +
	"2. Call add_artwork once per image, naming a themed collection (for example \"Christmas\"). The collection " +
	"is created when missing. Pass the image as an https image_url or as image_base64. For a search candidate, " +
	"pass its image_url, title, artist, license, and link so the credit is kept.\n" +
	"3. Call preview_artwork to see how a picture will look on the TV with the current crop or gallery-mat " +
	"settings. Regenerate any image add_artwork warns is too small or will be heavily cropped.\n" +
	"4. Call update_collection to schedule it: mode \"window\" with start and end dates as MM-DD (for example " +
	"12-01 to 01-06; windows may wrap the new year), or mode \"always\". New collections start \"off\", so " +
	"nothing reaches the TV until you schedule them.\n" +
	"5. Call sync_now to push the change to the TV, then get_status to watch the result.\n\n" +
	"Selection rules: art in no collection is the everyday rotation. While an exclusive collection (the default) " +
	"is active, only art in active collections is shown; a non-exclusive active collection is added to the " +
	"everyday rotation. Art that belongs only to inactive collections is hidden. Removing art from a collection " +
	"or deleting a collection never deletes image files."

// artwork is the artwork Collection and Sync Cycle authority the tools use.
type artwork interface {
	Prepare(context.Context, collection.PrepareRequest) (collection.Snapshot, error)
	Import(context.Context, collection.ImportRequest) (collection.Snapshot, error)
	RenderPreview(context.Context, collection.Item) (*image.RGBA, error)
	Placements() map[string][]string
	RequestSync() bool
}

// statusSource reports service and per-TV health.
type statusSource interface {
	Report() health.Report
}

// Options are the dependencies of the MCP handler.
type Options struct {
	Config  *config.Config
	Logger  *slog.Logger
	Artwork artwork
	Themes  *themes.Store
	Status  statusSource
	// Fetcher downloads image links. Nil uses a public-internet-only Fetcher.
	Fetcher *fetcher
	// Searcher finds public-domain museum art. Nil omits the search tool.
	Searcher artSearch
	// Now is the clock used for activation rules. Nil uses time.Now.
	Now func() time.Time
	// Version is reported to clients during initialization.
	Version string
}

type service struct {
	cfg      *config.Config
	logger   *slog.Logger
	artwork  artwork
	themes   *themes.Store
	status   statusSource
	fetcher  *fetcher
	searcher artSearch
	now      func() time.Time
	location *time.Location
}

// NewHandler returns the stateless streamable HTTP handler for /mcp. The
// caller is responsible for authentication and origin checks.
func NewHandler(options Options) (http.Handler, error) {
	if options.Config == nil || options.Artwork == nil || options.Themes == nil || options.Status == nil {
		return nil, errors.New("MCP handler requires configuration, artwork, themed collections, and status")
	}
	location, err := time.LoadLocation(options.Config.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load LOCATION_TIMEZONE for MCP: %w", err)
	}
	svc := &service{
		cfg: options.Config, logger: options.Logger, artwork: options.Artwork, themes: options.Themes,
		status: options.Status, fetcher: options.Fetcher, searcher: options.Searcher, now: options.Now, location: location,
	}
	if svc.logger == nil {
		svc.logger = slog.Default()
	}
	svc.logger = svc.logger.With("component", "mcp")
	if svc.fetcher == nil {
		svc.fetcher = newFetcher(fetcherOptions{})
	}
	if svc.now == nil {
		svc.now = time.Now
	}
	version := options.Version
	if version == "" {
		version = "dev"
	}
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Title: serverTitle, Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	svc.register(server)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Every request must carry MCP_TOKEN, which a DNS-rebinding page
		// cannot know, and reverse proxies on the same host legitimately send
		// a public Host header over loopback.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        svc.maxRequestBytes(),
	}), nil
}

func (svc *service) register(server *mcp.Server) {
	svc.registerSpecTools(server)
	svc.registerArtworkTools(server)
	svc.registerSearchTools(server)
	svc.registerCollectionTools(server)
	svc.registerStatusTools(server)
}

// maxImageBytes is the per-image limit shared with the web uploader.
func (svc *service) maxImageBytes() int64 {
	if mb := svc.cfg.MaxDownloadSizeMB; mb > 0 {
		return int64(mb) << 20
	}
	return defaultMaxMB << 20
}

// maxRequestBytes admits one base64-encoded image at the size limit plus
// the JSON-RPC envelope.
func (svc *service) maxRequestBytes() int64 {
	return int64(float64(svc.maxImageBytes())*base64Growth) + requestSlack
}

// today returns the current time in LOCATION_TIMEZONE.
func (svc *service) today() time.Time {
	return svc.now().In(svc.location)
}

// inventory returns a read-only, verified view of the Artwork Collection.
func (svc *service) inventory(ctx context.Context) (collection.Snapshot, error) {
	snapshot, err := svc.artwork.Prepare(ctx, collection.PrepareRequest{DryRun: true})
	if err != nil {
		return collection.Snapshot{}, fmt.Errorf("read the artwork folder: %w", err)
	}
	return snapshot, nil
}

// rejectDryRun refuses tools that would change saved state.
func (svc *service) rejectDryRun() error {
	if svc.cfg.DryRun {
		return themes.ErrDryRun
	}
	return nil
}

// findItem resolves an artwork reference: a filename, an artwork key, or a
// digest (full or at least 12 hex characters).
func findItem(items []collection.Item, reference string) (collection.Item, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return collection.Item{}, errors.New("name the artwork by filename, key, or digest")
	}
	for _, item := range items {
		if strings.EqualFold(item.Name, reference) || strings.EqualFold(item.Key, reference) {
			return item, nil
		}
	}
	lower := strings.ToLower(reference)
	if len(lower) >= shortDigestHx && isHex(lower) {
		var match *collection.Item
		for index := range items {
			if strings.HasPrefix(hex.EncodeToString(items[index].Digest[:]), lower) {
				if match != nil {
					return collection.Item{}, fmt.Errorf("digest prefix %q matches more than one artwork", reference)
				}
				match = &items[index]
			}
		}
		if match != nil {
			return *match, nil
		}
	}
	return collection.Item{}, fmt.Errorf("no artwork named %q; call list_artwork to see filenames and keys", reference)
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value + strings.Repeat("0", len(value)%2))
	return err == nil
}

func boolPointer(value bool) *bool {
	return &value
}
