package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// UnsplashClient handles communication with the Unsplash API.
type UnsplashClient struct {
	accessKey string
	client    *http.Client
	logger    *slog.Logger
}

// UnsplashPhoto represents the metadata returned by the Unsplash API.
type UnsplashPhoto struct {
	ID    string `json:"id"`
	Width int    `json:"width"`
	Height int   `json:"height"`
	Links struct {
		Download string `json:"download"`
		DownloadLocation string `json:"download_location"`
	} `json:"links"`
	URLs struct {
		Full string `json:"full"`
		Raw  string `json:"raw"`
	} `json:"urls"`
}

// NewUnsplashClient creates a new client for the Unsplash API.
func NewUnsplashClient(accessKey string, logger *slog.Logger) *UnsplashClient {
	return &UnsplashClient{
		accessKey: accessKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// FetchCollectionPhotos retrieves all photos from a specific Unsplash collection.
func (c *UnsplashClient) FetchCollectionPhotos(ctx context.Context, collectionID string) ([]UnsplashPhoto, error) {
	url := fmt.Sprintf("https://api.unsplash.com/collections/%s/photos?per_page=50", collectionID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Client-ID "+c.accessKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsplash api error: %d", resp.StatusCode)
	}

	var photos []UnsplashPhoto
	if err := json.NewDecoder(resp.Body).Decode(&photos); err != nil {
		return nil, fmt.Errorf("decode unsplash response: %w", err)
	}

	return photos, nil
}

// FetchPhoto retrieves metadata for a single Unsplash photo.
func (c *UnsplashClient) FetchPhoto(ctx context.Context, photoID string) (*UnsplashPhoto, error) {
	url := fmt.Sprintf("https://api.unsplash.com/photos/%s", photoID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Client-ID "+c.accessKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsplash api error: %d", resp.StatusCode)
	}

	var photo UnsplashPhoto
	if err := json.NewDecoder(resp.Body).Decode(&photo); err != nil {
		return nil, fmt.Errorf("decode unsplash response: %w", err)
	}

	return &photo, nil
}

// TrackDownload triggers the Unsplash "download" endpoint for a photo.
// This is required by the Unsplash API Terms of Service.
func (c *UnsplashClient) TrackDownload(ctx context.Context, downloadLocation string) {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadLocation, nil)
	if err != nil {
		return
	}

	req.Header.Set("Authorization", "Client-ID "+c.accessKey)

	resp, err := c.client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		c.logger.Debug("unsplash download tracked", "url", downloadLocation)
	}
}
