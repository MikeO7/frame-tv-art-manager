package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// pexelsProvider handles communication with the Pexels API and resolves artwork sources.
type pexelsProvider struct {
	apiKey  string
	client  *http.Client
	logger  *slog.Logger
	BaseURL string
}

// pexelsClient is a type alias for pexelsProvider to maintain backwards compatibility in tests.
type pexelsClient = pexelsProvider

// newPexelsClient creates a new Pexels API client/provider.
//
//nolint:unparam // complexity justified for this domain-specific path
func newPexelsClient(apiKey string, logger *slog.Logger) *pexelsClient {
	return newPexelsProvider(apiKey, logger)
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
//
//nolint:gocognit // complexity justified for this domain-specific path
func (p *pexelsProvider) FetchCollection(ctx context.Context, collectionID string) ([]string, error) {
	var allUrls []string
	page := 1

	for {
		apiURL := fmt.Sprintf("%s/v1/collections/%s?per_page=80&page=%d", p.BaseURL, url.PathEscape(collectionID), page)
		p.logger.Debug("fetching pexels collection page", "id", collectionID, "page", page)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", p.apiKey)

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("pexels api error: %d", resp.StatusCode)
		}

		var result struct {
			Media []pexelsPhoto `json:"media"`
			Page  int           `json:"page"`
		}
		maxBytes := int64(10 * 1024 * 1024) // 10MB limit
		reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
		decodeErr := json.NewDecoder(reader).Decode(&result)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode pexels response: %w", decodeErr)
		}

		if len(result.Media) == 0 {
			break
		}

		for _, ph := range result.Media {
			if ph.Src.Original != "" {
				allUrls = append(allUrls, ph.Src.Original)
			}
		}

		if len(result.Media) < 80 {
			break
		}
		page++

		if page > 10 {
			break
		}
	}

	return allUrls, nil
}

// FetchPhoto retrieves a single photo by its ID.
func (p *pexelsProvider) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	apiURL := fmt.Sprintf("%s/v1/photos/%s", p.BaseURL, url.PathEscape(photoID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pexels api error: %d", resp.StatusCode)
	}

	var photo pexelsPhoto
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&photo); err != nil {
		return "", fmt.Errorf("decode pexels response: %w", err)
	}

	return photo.Src.Original, nil
}

func (p *pexelsProvider) fetchPhotoList(ctx context.Context, apiURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pexels api error: %d", resp.StatusCode)
	}

	var result struct {
		Photos []pexelsPhoto `json:"photos"`
	}
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pexels response: %w", err)
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
		return nil, fmt.Errorf("invalid pexels format: %s", line)
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
		return nil, fmt.Errorf("unknown pexels type: %s", parts[1])
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
