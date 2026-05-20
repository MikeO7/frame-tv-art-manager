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

// articClient handles communication with the Art Institute of Chicago API.
type articClient struct {
	client      *http.Client
	logger      *slog.Logger
	BaseURL     string
	IIIFBaseURL string
}

// newArticClient creates a new Art Institute of Chicago API client.
func newArticClient(logger *slog.Logger) *articClient {
	return &articClient{
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

// Search Masterpieces from the Artic library.
func (c *articClient) Search(ctx context.Context, query string) ([]string, error) {
	// Search for artworks with an image_id (meaning they have a digitizable image)
	searchURL := fmt.Sprintf("%s/api/v1/artworks/search?q=%s&fields=id,title,image_id&limit=10", c.BaseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "FrameTVArtManager/1.0 (https://github.com/MikeO7/frame-tv-art-manager)")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artic api error: %d", resp.StatusCode)
	}

	var result struct {
		Data []articArtwork `json:"data"`
	}

	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode artic search response: %w", err)
	}

	var imageUrls []string
	for _, art := range result.Data {
		if art.ImageID != "" {
			// Construct the IIIF high-resolution URL (using !3840,2160 for best fit)
			imgURL := fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", c.IIIFBaseURL, art.ImageID)
			imageUrls = append(imageUrls, imgURL)
		}
	}

	return imageUrls, nil
}

// FetchPhoto retrieves a single masterpiece by its ID.
func (c *articClient) FetchPhoto(ctx context.Context, id string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/v1/artworks/%s?fields=id,title,image_id", c.BaseURL, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "FrameTVArtManager/1.0 (https://github.com/MikeO7/frame-tv-art-manager)")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("artic api error: %d", resp.StatusCode)
	}

	var result struct {
		Data articArtwork `json:"data"`
	}

	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
		return "", fmt.Errorf("decode artic response: %w", err)
	}

	if result.Data.ImageID == "" {
		return "", fmt.Errorf("artwork %s has no image_id", id)
	}

	return fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", c.IIIFBaseURL, result.Data.ImageID), nil
}
