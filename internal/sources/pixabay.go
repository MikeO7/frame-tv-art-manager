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

// PixabayClient handles communication with the Pixabay API.
type PixabayClient struct {
	apiKey string
	client *http.Client
	logger *slog.Logger
}

// PixabayPhoto represents the metadata returned by the Pixabay API.
type PixabayPhoto struct {
	ID            int    `json:"id"`
	PageURL       string `json:"pageURL"`
	LargeImageURL string `json:"largeImageURL"`
	FullHDURL     string `json:"fullHDURL"`
	ImageURL      string `json:"imageURL"` // Original high-res (requires approved access)
}

// NewPixabayClient creates a new client for the Pixabay API.
func NewPixabayClient(apiKey string, logger *slog.Logger) *PixabayClient {
	return &PixabayClient{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// Search retrieves photos from Pixabay based on a search query.
func (c *PixabayClient) Search(ctx context.Context, query string) ([]string, error) {
	u := fmt.Sprintf("https://pixabay.com/api/?key=%s&q=%s&per_page=10&image_type=photo", c.apiKey, url.QueryEscape(query))
	return c.fetchPhotoList(ctx, u)
}

// EditorsChoice retrieves the latest editor's choice photos from Pixabay.
func (c *PixabayClient) EditorsChoice(ctx context.Context) ([]string, error) {
	u := fmt.Sprintf("https://pixabay.com/api/?key=%s&editors_choice=true&per_page=10&image_type=photo", c.apiKey)
	return c.fetchPhotoList(ctx, u)
}

// FetchPhoto retrieves a single photo by its ID.
func (c *PixabayClient) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	u := fmt.Sprintf("https://pixabay.com/api/?key=%s&id=%s", c.apiKey, photoID)
	urls, err := c.fetchPhotoList(ctx, u)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", fmt.Errorf("pixabay photo not found: %s", photoID)
	}
	return urls[0], nil
}

// User retrieves the latest photos from a specific Pixabay user.
func (c *PixabayClient) User(ctx context.Context, userID string) ([]string, error) {
	u := fmt.Sprintf("https://pixabay.com/api/?key=%s&user_id=%s&per_page=50&image_type=photo", c.apiKey, userID)
	return c.fetchPhotoList(ctx, u)
}

func (c *PixabayClient) fetchPhotoList(ctx context.Context, apiURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pixabay api error: %d", resp.StatusCode)
	}

	var result struct {
		Hits []PixabayPhoto `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pixabay response: %w", err)
	}

	var urls []string
	for _, p := range result.Hits {
		// Prefer original high-res if available, then FullHD, then Large.
		best := p.ImageURL
		if best == "" {
			best = p.FullHDURL
		}
		if best == "" {
			best = p.LargeImageURL
		}
		if best != "" {
			urls = append(urls, best)
		}
	}
	return urls, nil
}
