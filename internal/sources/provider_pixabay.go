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

// pixabayProvider handles communication with the Pixabay API and resolves artwork sources.
type pixabayProvider struct {
	apiKey  string
	client  *http.Client
	logger  *slog.Logger
	BaseURL string
}

// pixabayClient is a type alias for pixabayProvider to maintain backwards compatibility in tests.
type pixabayClient = pixabayProvider

// newPixabayClient creates a new Pixabay API client/provider.
//
//nolint:unparam
func newPixabayClient(apiKey string, logger *slog.Logger) *pixabayClient {
	return newPixabayProvider(apiKey, logger)
}

// newPixabayProvider creates a new Pixabay provider.
func newPixabayProvider(apiKey string, logger *slog.Logger) *pixabayProvider {
	return &pixabayProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:  logger,
		BaseURL: "https://pixabay.com/api/",
	}
}

// pixabayPhoto represents the metadata returned by the Pixabay API.
type pixabayPhoto struct {
	ID            int    `json:"id"`
	PageURL       string `json:"pageURL"`
	LargeImageURL string `json:"largeImageURL"`
	FullHDURL     string `json:"fullHDURL"`
	ImageURL      string `json:"imageURL"` // Original high-res (requires approved access)
}

func (p *pixabayProvider) Name() string {
	return "pixabay"
}

func (p *pixabayProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "pixabay:")
}

// Search retrieves photos from Pixabay based on a search query with pagination.
func (p *pixabayProvider) Search(ctx context.Context, query string) ([]string, error) {
	return p.fetchAllPages(ctx, fmt.Sprintf("%s?key=%s&q=%s&image_type=photo", p.BaseURL, p.apiKey, url.QueryEscape(query)))
}

// EditorsChoice retrieves all editor's choice photos from Pixabay with pagination.
func (p *pixabayProvider) EditorsChoice(ctx context.Context) ([]string, error) {
	return p.fetchAllPages(ctx, fmt.Sprintf("%s?key=%s&editors_choice=true&image_type=photo", p.BaseURL, p.apiKey))
}

// FetchPhoto retrieves a single photo by its ID.
func (p *pixabayProvider) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	u := fmt.Sprintf("%s?key=%s&id=%s", p.BaseURL, p.apiKey, url.QueryEscape(photoID))
	urls, err := p.fetchPhotoList(ctx, u)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", fmt.Errorf("pixabay photo not found: %s", photoID)
	}
	return urls[0], nil
}

// User retrieves all photos from a specific Pixabay user with pagination.
func (p *pixabayProvider) User(ctx context.Context, userID string) ([]string, error) {
	return p.fetchAllPages(ctx, fmt.Sprintf("%s?key=%s&user_id=%s&image_type=photo", p.BaseURL, p.apiKey, url.QueryEscape(userID)))
}

func (p *pixabayProvider) fetchAllPages(ctx context.Context, baseURL string) ([]string, error) {
	var allUrls []string
	page := 1

	for {
		u := fmt.Sprintf("%s&per_page=200&page=%d", baseURL, page)
		p.logger.Debug("fetching pixabay page", "url", u, "page", page)

		urls, err := p.fetchPhotoList(ctx, u)
		if err != nil {
			return nil, err
		}

		if len(urls) == 0 {
			break
		}

		allUrls = append(allUrls, urls...)

		if len(urls) < 200 {
			break
		}
		page++

		if page > 5 {
			break
		}
	}

	return allUrls, nil
}

func (p *pixabayProvider) fetchPhotoList(ctx context.Context, apiURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pixabay api error: %d", resp.StatusCode)
	}

	var result struct {
		Hits []pixabayPhoto `json:"hits"`
	}
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pixabay response: %w", err)
	}

	var urls []string
	for _, hit := range result.Hits {
		best := hit.ImageURL
		if best == "" {
			best = hit.FullHDURL
		}
		if best == "" {
			best = hit.LargeImageURL
		}
		if best != "" {
			urls = append(urls, best)
		}
	}
	return urls, nil
}

func (p *pixabayProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid pixabay format: %s", line)
	}

	var urls []string
	var err error

	switch parts[1] {
	case cmdSearch:
		if len(parts) < 3 {
			return nil, fmt.Errorf("pixabay search requires a query: %s", line)
		}
		urls, err = p.Search(ctx, parts[2])
	case "editors_choice":
		urls, err = p.EditorsChoice(ctx)
	case cmdPhoto:
		if len(parts) < 3 {
			return nil, fmt.Errorf("pixabay photo requires an ID: %s", line)
		}
		var photoURL string
		photoURL, err = p.FetchPhoto(ctx, parts[2])
		if err == nil {
			urls = []string{photoURL}
		}
	case "user":
		if len(parts) < 3 {
			return nil, fmt.Errorf("pixabay user requires an ID: %s", line)
		}
		urls, err = p.User(ctx, parts[2])
	default:
		return nil, fmt.Errorf("unknown pixabay type: %s", parts[1])
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(urls))
	for _, u := range urls {
		slug := URLToSlug(u)
		idx := atomic.AddInt32(globalIndex, 1) - 1
		identity := fmt.Sprintf("%03d__pixabay__%s", idx, slug)

		images = append(images, SourceImage{
			URL:      u,
			Identity: identity,
		})
	}

	return images, nil
}
