package sources

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// articMaxResponseBytes caps a decoded Art Institute API response to bound memory.
const articMaxResponseBytes = 10 << 20 // 10 MiB

// articProvider handles communication with the Art Institute of Chicago API and resolves artwork sources.
type articProvider struct {
	client      *http.Client
	logger      *slog.Logger
	BaseURL     string
	IIIFBaseURL string
}

// newArticProvider creates a new Art Institute of Chicago provider.
func newArticProvider(logger *slog.Logger) *articProvider {
	return &articProvider{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:      logger,
		BaseURL:     "https://api.artic.edu",
		IIIFBaseURL: "https://www.artic.edu/iiif/2",
	}
}

// articArtwork represents a masterpiece returned by the Artic API.
type articArtwork struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	ImageID string `json:"image_id"`
}

// fetchJSON issues a GET (with the shared User-Agent) to apiURL and decodes the
// size-bounded JSON response body into out, centralizing request boilerplate.
func (p *articProvider) fetchJSON(ctx context.Context, apiURL, errLabel string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, url: apiURL, headers: map[string]string{"User-Agent": userAgent},
		maxBytes: articMaxResponseBytes, statusLabel: "artic api", decodeLabel: errLabel,
	}, out)
}

// iiifImageURL builds the IIIF 4K full-image URL for an Art Institute image ID.
func (p *articProvider) iiifImageURL(imageID string) string {
	return fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", p.IIIFBaseURL, imageID)
}

func (p *articProvider) Name() string {
	return "artic"
}

func (p *articProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "artic:") || strings.HasPrefix(line, "art_institute:") || strings.HasPrefix(line, "art_institute_of_chicago:")
}

// Search Masterpieces from the Artic library.
func (p *articProvider) Search(ctx context.Context, query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/api/v1/artworks/search?q=%s&fields=id,title,image_id&limit=10", p.BaseURL, url.QueryEscape(query))

	var result struct {
		Data []articArtwork `json:"data"`
	}
	if err := p.fetchJSON(ctx, searchURL, "artic search", &result); err != nil {
		return nil, err
	}

	var imageUrls []string
	for _, art := range result.Data {
		if art.ImageID != "" {
			imageUrls = append(imageUrls, p.iiifImageURL(art.ImageID))
		}
	}

	return imageUrls, nil
}

// FetchPhoto retrieves a single masterpiece by its ID.
func (p *articProvider) FetchPhoto(ctx context.Context, id string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/v1/artworks/%s?fields=id,title,image_id", p.BaseURL, url.PathEscape(id))

	var result struct {
		Data articArtwork `json:"data"`
	}
	if err := p.fetchJSON(ctx, apiURL, "artic", &result); err != nil {
		return "", err
	}

	if result.Data.ImageID == "" {
		return "", fmt.Errorf("artwork has no image_id")
	}

	return p.iiifImageURL(result.Data.ImageID), nil
}

func (p *articProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid art_institute_of_chicago format")
	}

	var urls []string
	var err error

	switch parts[1] {
	case cmdSearch:
		urls, err = p.Search(ctx, parts[2])
	case cmdPhoto:
		urlStr, fetchErr := p.FetchPhoto(ctx, parts[2])
		if fetchErr == nil {
			urls = []string{urlStr}
		}
		err = fetchErr
	default:
		return nil, fmt.Errorf("unknown artic type")
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(urls))
	for _, u := range urls {
		slug := slugFromArticURL(u)

		idx := atomic.AddInt32(globalIndex, 1) - 1
		identity := fmt.Sprintf("%03d__artic__%s", idx, slug)

		images = append(images, SourceImage{
			URL:      u,
			Identity: identity,
		})
	}

	return images, nil
}
