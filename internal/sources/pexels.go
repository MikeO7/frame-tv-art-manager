package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// PexelsClient handles communication with the Pexels API.
type PexelsClient struct {
	apiKey string
	client *http.Client
	logger *slog.Logger
}

// PexelsPhoto represents the metadata returned by the Pexels API.
type PexelsPhoto struct {
	ID  int    `json:"id"`
	Url string `json:"url"`
	Src struct {
		Original string `json:"original"`
		Large2x  string `json:"large2x"`
		Large    string `json:"large"`
	} `json:"src"`
}

// NewPexelsClient creates a new client for the Pexels API.
func NewPexelsClient(apiKey string, logger *slog.Logger) *PexelsClient {
	return &PexelsClient{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// Search retrieves photos from Pexels based on a search query.
func (c *PexelsClient) Search(ctx context.Context, query string) ([]string, error) {
	url := fmt.Sprintf("https://api.pexels.com/v1/search?query=%s&per_page=10", query)
	return c.fetchPhotoList(ctx, url)
}

// Curated retrieves the latest curated photos from Pexels.
func (c *PexelsClient) Curated(ctx context.Context) ([]string, error) {
	url := "https://api.pexels.com/v1/curated?per_page=10"
	return c.fetchPhotoList(ctx, url)
}

// FetchCollection retrieves photos from a specific Pexels collection.
func (c *PexelsClient) FetchCollection(ctx context.Context, collectionID string) ([]string, error) {
	url := fmt.Sprintf("https://api.pexels.com/v1/collections/%s?per_page=15", collectionID)
	// Pexels collection response structure is slightly different (it has 'media' instead of 'photos')
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
		Media []PexelsPhoto `json:"media"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pexels response: %w", err)
	}
	var urls []string
	for _, p := range result.Media {
		urls = append(urls, p.Src.Original)
	}
	return urls, nil
}

// FetchPhoto retrieves a single photo by its ID.
func (c *PexelsClient) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	url := fmt.Sprintf("https://api.pexels.com/v1/photos/%s", photoID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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

	var photo PexelsPhoto
	if err := json.NewDecoder(resp.Body).Decode(&photo); err != nil {
		return "", fmt.Errorf("decode pexels response: %w", err)
	}

	return photo.Src.Original, nil
}

func (c *PexelsClient) fetchPhotoList(ctx context.Context, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
		Photos []PexelsPhoto `json:"photos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pexels response: %w", err)
	}

	var urls []string
	for _, p := range result.Photos {
		urls = append(urls, p.Src.Original)
	}
	return urls, nil
}
