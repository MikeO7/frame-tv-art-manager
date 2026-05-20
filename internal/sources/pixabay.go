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

// pixabayClient handles communication with the Pixabay API.
type pixabayClient struct {
	apiKey  string
	client  *http.Client
	logger  *slog.Logger
	BaseURL string
}

// pixabayPhoto represents the metadata returned by the Pixabay API.
type pixabayPhoto struct {
	ID            int    `json:"id"`
	PageURL       string `json:"pageURL"`
	LargeImageURL string `json:"largeImageURL"`
	FullHDURL     string `json:"fullHDURL"`
	ImageURL      string `json:"imageURL"` // Original high-res (requires approved access)
}

func newPixabayClient(apiKey string, logger *slog.Logger) *pixabayClient {
	return &pixabayClient{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:  logger,
		BaseURL: "https://pixabay.com/api/",
	}
}

// Search retrieves photos from Pixabay based on a search query with pagination.
func (c *pixabayClient) Search(ctx context.Context, query string) ([]string, error) {
	return c.fetchAllPages(ctx, fmt.Sprintf("%s?key=%s&q=%s&image_type=photo", c.BaseURL, c.apiKey, url.QueryEscape(query)))
}

// EditorsChoice retrieves all editor's choice photos from Pixabay with pagination.
func (c *pixabayClient) EditorsChoice(ctx context.Context) ([]string, error) {
	return c.fetchAllPages(ctx, fmt.Sprintf("%s?key=%s&editors_choice=true&image_type=photo", c.BaseURL, c.apiKey))
}

// FetchPhoto retrieves a single photo by its ID.
func (c *pixabayClient) FetchPhoto(ctx context.Context, photoID string) (string, error) {
	u := fmt.Sprintf("%s?key=%s&id=%s", c.BaseURL, c.apiKey, url.QueryEscape(photoID))
	urls, err := c.fetchPhotoList(ctx, u)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", fmt.Errorf("pixabay photo not found: %s", photoID)
	}
	return urls[0], nil
}

// User retrieves all photos from a specific Pixabay user with pagination.
func (c *pixabayClient) User(ctx context.Context, userID string) ([]string, error) {
	return c.fetchAllPages(ctx, fmt.Sprintf("%s?key=%s&user_id=%s&image_type=photo", c.BaseURL, c.apiKey, url.QueryEscape(userID)))
}

func (c *pixabayClient) fetchAllPages(ctx context.Context, baseURL string) ([]string, error) {
	var allUrls []string
	page := 1

	for {
		u := fmt.Sprintf("%s&per_page=200&page=%d", baseURL, page)
		c.logger.Debug("fetching pixabay page", "url", u, "page", page)

		urls, err := c.fetchPhotoList(ctx, u)
		if err != nil {
			return nil, err
		}

		if len(urls) == 0 {
			break
		}

		allUrls = append(allUrls, urls...)

		// If we got fewer than 200, we are at the end.
		if len(urls) < 200 {
			break
		}
		page++

		// Safety cap.
		if page > 5 { // 1000 images limit.
			break
		}
	}

	return allUrls, nil
}

func (c *pixabayClient) fetchPhotoList(ctx context.Context, apiURL string) ([]string, error) {
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
		Hits []pixabayPhoto `json:"hits"`
	}
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
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
