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

// UnsplashClient provides a minimal Unsplash API client for fetching
// photo download URLs. Only used if UNSPLASH_ACCESS_KEY is configured.
//
// Unsplash API docs: https://unsplash.com/documentation
// Rate limits: 50 req/hr (demo), 5000 req/hr (production)
type UnsplashClient struct {
	accessKey string
	client    *http.Client
	logger    *slog.Logger
}

// UnsplashPhoto represents a single photo from the Unsplash API.
type UnsplashPhoto struct {
	ID   string `json:"id"`
	URLs struct {
		Raw     string `json:"raw"`
		Full    string `json:"full"`
		Regular string `json:"regular"` // 1080px wide
	} `json:"urls"`
	User struct {
		Name string `json:"name"`
	} `json:"user"`
	Description string `json:"description"`
}

// NewUnsplashClient creates a client for the Unsplash API.
// Returns nil if accessKey is empty (feature disabled).
func NewUnsplashClient(accessKey string, logger *slog.Logger) *UnsplashClient {
	if accessKey == "" {
		return nil
	}
	return &UnsplashClient{
		accessKey: accessKey,
		client:    &http.Client{Timeout: 30 * time.Second},
		logger:    logger,
	}
}

// RandomPhotos fetches random photos from Unsplash, optionally filtered
// by a search query. Returns download URLs suitable for the sources loader.
//
// count is capped at 30 per Unsplash API limits.
func (u *UnsplashClient) RandomPhotos(ctx context.Context, query string, count int) ([]string, error) {
	if count > 30 {
		count = 30
	}
	if count < 1 {
		count = 1
	}

	params := url.Values{}
	params.Set("count", fmt.Sprintf("%d", count))
	params.Set("orientation", "landscape") // Frame TVs are landscape
	if query != "" {
		params.Set("query", query)
	}

	apiURL := fmt.Sprintf("https://api.unsplash.com/photos/random?%s", params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Client-ID "+u.accessKey)
	req.Header.Set("Accept-Version", "v1")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unsplash API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		remaining := resp.Header.Get("X-Ratelimit-Remaining")
		return nil, fmt.Errorf("unsplash rate limited (remaining: %s)", remaining)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsplash API returned %d", resp.StatusCode)
	}

	var photos []UnsplashPhoto
	if err := json.NewDecoder(resp.Body).Decode(&photos); err != nil {
		return nil, fmt.Errorf("parse unsplash response: %w", err)
	}

	// Build download URLs — use "raw" with width parameter for 4K resolution.
	urls := make([]string, 0, len(photos))
	for _, p := range photos {
		// Append width parameter for optimal Frame TV resolution (3840px).
		downloadURL := p.URLs.Raw + "&w=3840&q=85&fm=jpg"
		urls = append(urls, downloadURL)
		u.logger.Debug("unsplash photo",
			"id", p.ID,
			"photographer", p.User.Name,
		)
	}

	u.logger.Info("fetched photos from Unsplash",
		"count", len(urls),
		"query", query,
	)

	return urls, nil
}

// CollectionPhotos fetches all photos from an Unsplash collection.
func (u *UnsplashClient) CollectionPhotos(ctx context.Context, collectionID string, perPage int) ([]string, error) {
	if perPage < 1 || perPage > 30 {
		perPage = 30
	}

	apiURL := fmt.Sprintf("https://api.unsplash.com/collections/%s/photos?per_page=%d&orientation=landscape",
		collectionID, perPage)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Client-ID "+u.accessKey)
	req.Header.Set("Accept-Version", "v1")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unsplash collection request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsplash collection API returned %d", resp.StatusCode)
	}

	var photos []UnsplashPhoto
	if err := json.NewDecoder(resp.Body).Decode(&photos); err != nil {
		return nil, fmt.Errorf("parse collection response: %w", err)
	}

	urls := make([]string, 0, len(photos))
	for _, p := range photos {
		downloadURL := p.URLs.Raw + "&w=3840&q=85&fm=jpg"
		urls = append(urls, downloadURL)
	}

	u.logger.Info("fetched collection photos from Unsplash",
		"collection", collectionID,
		"count", len(urls),
	)

	return urls, nil
}
