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

// UnsplashClient handles communication with the Unsplash API.
type UnsplashClient struct {
	appID     string
	accessKey string
	secretKey string
	client    *http.Client
	logger    *slog.Logger
	BaseURL   string
}

// UnsplashPhoto represents the metadata returned by the Unsplash API.
type UnsplashPhoto struct {
	ID     string `json:"id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Links  struct {
		Download         string `json:"download"`
		DownloadLocation string `json:"download_location"`
	} `json:"links"`
	URLs struct {
		Full string `json:"full"`
		Raw  string `json:"raw"`
	} `json:"urls"`
}

// NewUnsplashClient creates a new client for the Unsplash API.
func NewUnsplashClient(appID, accessKey, secretKey string, logger *slog.Logger) *UnsplashClient {
	return &UnsplashClient{
		appID:     appID,
		accessKey: accessKey,
		secretKey: secretKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:  logger,
		BaseURL: "https://api.unsplash.com",
	}
}

// FetchCollectionPhotos retrieves all photos from a specific Unsplash collection using pagination.
func (c *UnsplashClient) FetchCollectionPhotos(ctx context.Context, collectionID string) ([]UnsplashPhoto, error) {
	var allPhotos []UnsplashPhoto
	page := 1

	for {
		url := fmt.Sprintf("%s/collections/%s/photos?per_page=30&page=%d", c.BaseURL, collectionID, page)
		c.logger.Debug("fetching unsplash collection page", "id", collectionID, "page", page)

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

		var pagePhotos []UnsplashPhoto
		if err := json.NewDecoder(resp.Body).Decode(&pagePhotos); err != nil {
			return nil, fmt.Errorf("decode unsplash response: %w", err)
		}

		if len(pagePhotos) == 0 {
			break
		}

		allPhotos = append(allPhotos, pagePhotos...)

		// If we got fewer than 30, we are at the end.
		if len(pagePhotos) < 30 {
			break
		}
		page++

		// Safety cap to prevent infinite loops (max 33 pages / ~1000 images)
		if page > 33 {
			break
		}
	}

	return allPhotos, nil
}

// FetchPhoto retrieves metadata for a single Unsplash photo.
func (c *UnsplashClient) FetchPhoto(ctx context.Context, photoID string) (*UnsplashPhoto, error) {
	url := fmt.Sprintf("%s/photos/%s", c.BaseURL, photoID)
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
	// Prevent SSRF and API key leakage by validating the domain.
	parsedURL, err := url.Parse(downloadLocation)
	if err != nil {
		c.logger.Warn("invalid unsplash download location URL format", "url", downloadLocation)
		return
	}
	baseURL, err := url.Parse(c.BaseURL)
	if err != nil || parsedURL.Host != baseURL.Host {
		c.logger.Warn("invalid unsplash download location host", "url", downloadLocation)
		return
	}

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
