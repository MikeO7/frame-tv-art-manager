package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// pexelsClient handles communication with the Pexels API.
type pexelsClient struct {
	apiKey  string
	client  *http.Client
	logger  *slog.Logger
	BaseURL string
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

// newPexelsClient creates a new Pexels API client.
func newPexelsClient(apiKey string, logger *slog.Logger) *pexelsClient {
	return &pexelsClient{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:  logger,
		BaseURL: "https://api.pexels.com",
	}
}

// Search retrieves photos from Pexels based on a search query.
func (c *pexelsClient) Search(ctx context.Context, query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/v1/search?query=%s&per_page=10", c.BaseURL, url.QueryEscape(query))
	return c.fetchPhotoList(ctx, searchURL)
}

// Curated retrieves the latest curated photos from Pexels.
func (c *pexelsClient) Curated(ctx context.Context) ([]string, error) {
	apiURL := fmt.Sprintf("%s/v1/curated?per_page=10", c.BaseURL)
	return c.fetchPhotoList(ctx, apiURL)
}

// FetchCollection retrieves all photos from a specific Pexels collection using pagination.
func (c *pexelsClient) FetchCollection(ctx context.Context, collectionID string) ([]string, error) {
	var allUrls []string
	page := 1

	for {
		apiURL := fmt.Sprintf("%s/v1/collections/%s?per_page=80&page=%d", c.BaseURL, url.PathEscape(collectionID), page)
		c.logger.Debug("fetching pexels collection page", "id", collectionID, "page", page)

		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", c.apiKey)

		resp, err := c.client.Do(req)
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

		for _, p := range result.Media {
			if p.Src.Original != "" {
				allUrls = append(allUrls, p.Src.Original)
			}
		}

		// If we got fewer than 80, we are at the end.
		if len(result.Media) < 80 {
			break
		}
		page++

		// Safety cap to prevent infinite loops (max 10 pages / 800 images)
		if page > 10 {
			break
		}
	}

	return allUrls, nil
}

// FetchPhoto retrieves a single photo by its ID.
func (c *pexelsClient) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	apiURL := fmt.Sprintf("%s/v1/photos/%s", c.BaseURL, url.PathEscape(photoID))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.client.Do(req)
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

func (c *pexelsClient) fetchPhotoList(ctx context.Context, apiURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.client.Do(req)
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

	//nolint:prealloc
	var urls []string
	for _, p := range result.Photos {
		urls = append(urls, p.Src.Original)
	}
	return urls, nil
}
