package sources

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// pixabayMaxResponseBytes caps a decoded Pixabay API response to bound memory.
	pixabayMaxResponseBytes = 10 << 20 // 10 MiB
	// pixabayPageSize is the per-page item count requested (Pixabay's max).
	pixabayPageSize = 200
	// pixabayMaxResults is the most hits Pixabay serves for one query.
	pixabayMaxResults = 500
	// pixabayMaxQueryRunes is Pixabay's documented limit for the q parameter.
	pixabayMaxQueryRunes = 100
	// pixabayMinWidth keeps searches to originals wide enough for a 4K panel.
	pixabayMinWidth = "3840"
	pixabayLicense  = "Pixabay Content License"
)

// pixabayProvider handles communication with the Pixabay API and resolves artwork sources.
type pixabayProvider struct {
	apiKey  string
	client  *http.Client
	gate    *rateGate
	logger  *slog.Logger
	BaseURL string
}

// newPixabayProvider creates a new Pixabay provider.
func newPixabayProvider(apiKey string, logger *slog.Logger) *pixabayProvider {
	return &pixabayProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		gate:    newRateGate(),
		logger:  logger,
		BaseURL: "https://pixabay.com/api/",
	}
}

// pixabayPhoto represents the metadata returned by the Pixabay API.
type pixabayPhoto struct {
	ID            int    `json:"id"`
	PageURL       string `json:"pageURL"`
	Tags          string `json:"tags"`
	User          string `json:"user"`
	LargeImageURL string `json:"largeImageURL"`
	FullHDURL     string `json:"fullHDURL"`
	ImageURL      string `json:"imageURL"` // Original high-res (requires approved access)
}

type pixabayPage struct {
	TotalHits int            `json:"totalHits"`
	Hits      []pixabayPhoto `json:"hits"`
}

func (p *pixabayProvider) Name() string {
	return providerPixabay
}

func (p *pixabayProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "pixabay:")
}

func (p *pixabayProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 {
		return nil, errors.New("invalid pixabay format")
	}
	argument := ""
	if len(parts) == 3 {
		argument = parts[2]
	}

	query := url.Values{"image_type": {"photo"}, "min_width": {pixabayMinWidth}}
	switch parts[1] {
	case cmdSearch:
		if argument == "" {
			return nil, errors.New("pixabay search requires a query")
		}
		query.Set("q", truncateRunes(argument, pixabayMaxQueryRunes))
	case "editors_choice":
		query.Set("editors_choice", "true")
	case cmdPhoto:
		if argument == "" {
			return nil, errors.New("pixabay photo requires an ID")
		}
		return p.resolvePhoto(ctx, argument)
	case "user":
		return nil, errors.New("pixabay user sources are not supported because the Pixabay API cannot filter by user; " +
			"use pixabay:photo:<id> or pixabay:search:<query>")
	default:
		return nil, errors.New("unknown pixabay type")
	}
	photos, err := p.fetchAllPages(ctx, query)
	if err != nil {
		return nil, err
	}
	return p.images(photos), nil
}

func (p *pixabayProvider) resolvePhoto(ctx context.Context, photoID string) ([]sourceImage, error) {
	page, err := p.fetchPage(ctx, url.Values{"id": {photoID}})
	if err != nil {
		return nil, err
	}
	images := p.images(page.Hits)
	if len(images) == 0 {
		return nil, errors.New("pixabay photo not found")
	}
	return images[:1], nil
}

// fetchAllPages follows pagination only while Pixabay reports more
// accessible hits; requesting a page beyond totalHits is rejected by the API.
func (p *pixabayProvider) fetchAllPages(ctx context.Context, query url.Values) ([]pixabayPhoto, error) {
	var photos []pixabayPhoto
	query.Set("per_page", strconv.Itoa(pixabayPageSize))
	for page := 1; (page-1)*pixabayPageSize < pixabayMaxResults; page++ {
		p.logger.Debug("fetching pixabay page", "page", page)
		query.Set("page", strconv.Itoa(page))
		result, err := p.fetchPage(ctx, query)
		if err != nil {
			return nil, err
		}
		photos = append(photos, result.Hits...)
		available := min(result.TotalHits, pixabayMaxResults)
		if len(result.Hits) < pixabayPageSize || page*pixabayPageSize >= available {
			break
		}
	}
	return photos, nil
}

func (p *pixabayProvider) fetchPage(ctx context.Context, query url.Values) (pixabayPage, error) {
	authorized := maps.Clone(query)
	authorized.Set("key", p.apiKey)
	var result pixabayPage
	err := fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: p.BaseURL + "?" + authorized.Encode(),
		maxBytes: pixabayMaxResponseBytes, statusLabel: "pixabay api", decodeLabel: "pixabay",
	}, &result)
	return result, err
}

// images converts hits to source images, preferring the highest resolution
// the API key may access. Pixabay download URLs are temporary, so identity
// is derived from the stable photo ID.
func (p *pixabayProvider) images(hits []pixabayPhoto) []sourceImage {
	images := make([]sourceImage, 0, len(hits))
	for _, hit := range hits {
		var candidates []string
		for _, candidate := range []string{hit.ImageURL, hit.FullHDURL, hit.LargeImageURL} {
			if candidate != "" {
				candidates = append(candidates, candidate)
			}
		}
		if len(candidates) == 0 || hit.ID <= 0 {
			p.logger.Warn("skipping pixabay hit without a downloadable image", "id", hit.ID)
			continue
		}
		id := strconv.Itoa(hit.ID)
		images = append(images, sourceImage{
			URL: candidates[0], Fallbacks: candidates[1:], Identity: sourceIdentity(p.Name(), "photo:"+id, "photo-"+id),
			Attribution: attribution{
				Provider: p.Name(), Title: hit.Tags, Artist: hit.User, SourceURL: hit.PageURL, License: pixabayLicense,
			},
		})
	}
	return images
}

// truncateRunes shortens value to at most limit runes.
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
