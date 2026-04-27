package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ArticClient handles communication with the Art Institute of Chicago API.
type ArticClient struct {
	client *http.Client
	logger *slog.Logger
}

// NewArticClient creates a new Art Institute of Chicago API client.
func NewArticClient(logger *slog.Logger) *ArticClient {
	return &ArticClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// ArticArtwork represents a masterpiece returned by the Artic API.
type ArticArtwork struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	ImageID string `json:"image_id"`
}

// Search Masterpieces from the Artic library.
func (c *ArticClient) Search(ctx context.Context, query string) ([]string, error) {
	// Search for artworks with an image_id (meaning they have a digitizable image)
	url := fmt.Sprintf("https://api.artic.edu/api/v1/artworks/search?q=%s&fields=id,title,image_id&limit=10", query)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artic api error: %d", resp.StatusCode)
	}

	var result struct {
		Data []ArticArtwork `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode artic search response: %w", err)
	}

	var imageUrls []string
	for _, art := range result.Data {
		if art.ImageID != "" {
			// Construct the IIIF high-resolution URL (requesting 3840px width for 4K)
			imgURL := fmt.Sprintf("https://www.artic.edu/iiif/2/%s/full/3840,/0/default.jpg", art.ImageID)
			imageUrls = append(imageUrls, imgURL)
		}
	}

	return imageUrls, nil
}

// FetchPhoto retrieves a single masterpiece by its ID.
func (c *ArticClient) FetchPhoto(ctx context.Context, id string) (string, error) {
	url := fmt.Sprintf("https://api.artic.edu/api/v1/artworks/%s?fields=id,title,image_id", id)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("artic api error: %d", resp.StatusCode)
	}

	var result struct {
		Data ArticArtwork `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode artic response: %w", err)
	}

	if result.Data.ImageID == "" {
		return "", fmt.Errorf("artwork %s has no image_id", id)
	}

	return fmt.Sprintf("https://www.artic.edu/iiif/2/%s/full/3840,/0/default.jpg", result.Data.ImageID), nil
}
