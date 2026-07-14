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

const (
	// pexelsMaxResponseBytes caps a decoded Pexels API response to guard against
	// unbounded memory use from a hostile or malfunctioning endpoint.
	pexelsMaxResponseBytes = 10 << 20 // 10 MiB
	// pexelsCollectionPageSize is the per-page item count requested for collections.
	pexelsCollectionPageSize = 80
	// pexelsMaxPages bounds collection pagination to avoid unbounded crawling.
	pexelsMaxPages = 10
)

// pexelsProvider handles communication with the Pexels API and resolves artwork sources.
type pexelsProvider struct {
	apiKey  string
	client  *http.Client
	logger  *slog.Logger
	BaseURL string
}

// newPexelsProvider creates a new Pexels provider.
func newPexelsProvider(apiKey string, logger *slog.Logger) *pexelsProvider {
	return &pexelsProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:  logger,
		BaseURL: "https://api.pexels.com",
	}
}

// pexelsPhoto represents the metadata returned by the Pexels API.
type pexelsPhoto struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
	Src struct {
		Original string `json:"original"`
		Large2x  string `json:"large2x"`
		Large    string `json:"large"`
	} `json:"src"`
}

// fetchJSON issues an authorized GET to apiURL and decodes the size-bounded
// JSON response body into out, centralizing the Pexels request boilerplate.
func (p *pexelsProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, url: apiURL, headers: map[string]string{headerAuthorization: p.apiKey},
		maxBytes: pexelsMaxResponseBytes, statusLabel: "pexels api", decodeLabel: "pexels",
	}, out)
}

func (p *pexelsProvider) Name() string {
	return providerPexels
}

func (p *pexelsProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "pexels:")
}

// Search retrieves photos from Pexels based on a search query.
func (p *pexelsProvider) Search(ctx context.Context, query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/v1/search?query=%s&per_page=10", p.BaseURL, url.QueryEscape(query))
	return p.fetchPhotoList(ctx, searchURL)
}

// Curated retrieves the latest curated photos from Pexels.
func (p *pexelsProvider) Curated(ctx context.Context) ([]string, error) {
	apiURL := fmt.Sprintf("%s/v1/curated?per_page=10", p.BaseURL)
	return p.fetchPhotoList(ctx, apiURL)
}

// FetchCollection retrieves all photos from a specific Pexels collection using pagination.
func (p *pexelsProvider) FetchCollection(ctx context.Context, collectionID string) ([]string, error) {
	var allUrls []string
	for page := 1; page <= pexelsMaxPages; page++ {
		urls, more, err := p.fetchCollectionPage(ctx, collectionID, page)
		if err != nil {
			return nil, err
		}
		allUrls = append(allUrls, urls...)
		if !more {
			break
		}
	}
	return allUrls, nil
}

// fetchCollectionPage retrieves a single page of a Pexels collection and
// reports whether a further page may exist (i.e. this page was full).
func (p *pexelsProvider) fetchCollectionPage(ctx context.Context, collectionID string, page int) ([]string, bool, error) {
	apiURL := fmt.Sprintf("%s/v1/collections/%s?per_page=%d&page=%d", p.BaseURL, url.PathEscape(collectionID), pexelsCollectionPageSize, page)
	p.logger.Debug("fetching pexels collection page", "id", collectionID, "page", page)

	var result struct {
		Media []pexelsPhoto `json:"media"`
		Page  int           `json:"page"`
	}
	if err := p.fetchJSON(ctx, apiURL, &result); err != nil {
		return nil, false, err
	}

	urls := make([]string, 0, len(result.Media))
	for _, ph := range result.Media {
		if ph.Src.Original != "" {
			urls = append(urls, ph.Src.Original)
		}
	}
	return urls, len(result.Media) >= pexelsCollectionPageSize, nil
}

// FetchPhoto retrieves a single photo by its ID.
func (p *pexelsProvider) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	apiURL := fmt.Sprintf("%s/v1/photos/%s", p.BaseURL, url.PathEscape(photoID))

	var photo pexelsPhoto
	if err := p.fetchJSON(ctx, apiURL, &photo); err != nil {
		return "", err
	}

	return photo.Src.Original, nil
}

func (p *pexelsProvider) fetchPhotoList(ctx context.Context, apiURL string) ([]string, error) {
	var result struct {
		Photos []pexelsPhoto `json:"photos"`
	}
	if err := p.fetchJSON(ctx, apiURL, &result); err != nil {
		return nil, err
	}

	urls := make([]string, 0, len(result.Photos))
	for _, ph := range result.Photos {
		urls = append(urls, ph.Src.Original)
	}
	return urls, nil
}

func (p *pexelsProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid pexels format")
	}

	var urls []string
	var err error

	switch parts[1] {
	case cmdSearch:
		if len(parts) < 3 {
			return nil, fmt.Errorf("pexels search requires a query")
		}
		urls, err = p.Search(ctx, parts[2])
	case "curated":
		urls, err = p.Curated(ctx)
	case "collection":
		if len(parts) < 3 {
			return nil, fmt.Errorf("pexels collection requires an ID")
		}
		urls, err = p.FetchCollection(ctx, parts[2])
	case cmdPhoto:
		if len(parts) < 3 {
			return nil, fmt.Errorf("pexels photo requires an ID")
		}
		var photoURL string
		photoURL, err = p.FetchPhoto(ctx, parts[2])
		if err == nil {
			urls = []string{photoURL}
		}
	default:
		return nil, fmt.Errorf("unknown pexels type")
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(urls))
	for _, u := range urls {
		slug := URLToSlug(u)
		idx := atomic.AddInt32(globalIndex, 1) - 1
		identity := fmt.Sprintf("%03d__pexels__%s", idx, slug)

		images = append(images, SourceImage{
			URL:      u,
			Identity: identity,
		})
	}

	return images, nil
}
