package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// NASAClient handles communication with NASA APIs.
type NASAClient struct {
	apiKey    string
	client    *http.Client
	logger    *slog.Logger
	BaseURL   string
	SearchURL string
}

// NewNASAClient creates a new NASA API client.
func NewNASAClient(apiKey string, logger *slog.Logger) *NASAClient {
	if apiKey == "" {
		apiKey = "DEMO_KEY"
	}
	return &NASAClient{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:    logger,
		BaseURL:   "https://api.nasa.gov",
		SearchURL: "https://images-api.nasa.gov",
	}
}

// APODResponse represents the response from NASA's APOD API.
type APODResponse struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	HDURL string `json:"hdurl"`
	Type  string `json:"media_type"`
}

// FetchAPOD retrieves today's Astronomy Picture of the Day.
func (c *NASAClient) FetchAPOD(ctx context.Context) (*APODResponse, error) {
	url := fmt.Sprintf("%s/planetary/apod?api_key=%s", c.BaseURL, c.apiKey)
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
		return nil, fmt.Errorf("nasa apod api error: %d", resp.StatusCode)
	}

	var apod APODResponse
	if err := json.NewDecoder(resp.Body).Decode(&apod); err != nil {
		return nil, fmt.Errorf("decode nasa apod response: %w", err)
	}

	if apod.Type != "image" {
		return nil, fmt.Errorf("today's apod is a %s, not an image", apod.Type)
	}

	return &apod, nil
}

// SearchNASAImageLibrary searches for high-resolution images in the NASA library.
func (c *NASAClient) SearchNASAImageLibrary(ctx context.Context, query string) ([]string, error) {
	url := fmt.Sprintf("%s/search?q=%s&media_type=image", c.SearchURL, query)
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
		return nil, fmt.Errorf("nasa image library error: %d", resp.StatusCode)
	}

	var result struct {
		Collection struct {
			Items []struct {
				Href string `json:"href"` // This is the manifest URL
				Data []struct {
					NASAID string `json:"nasa_id"`
					Title  string `json:"title"`
				} `json:"data"`
			} `json:"items"`
		} `json:"collection"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode nasa search response: %w", err)
	}

	var imageUrls []string
	// Limit to first 10 results for search to avoid huge downloads
	maxItems := 10
	if len(result.Collection.Items) < maxItems {
		maxItems = len(result.Collection.Items)
	}

	for i := 0; i < maxItems; i++ {
		item := result.Collection.Items[i]
		// Each item has a manifest URL (href) which contains the actual image links.
		manifestURL, err := c.fetchNASAAssetManifest(ctx, item.Href)
		if err != nil {
			c.logger.Warn("failed to fetch nasa asset manifest", "href", item.Href, "error", err)
			continue
		}
		if manifestURL != "" {
			imageUrls = append(imageUrls, manifestURL)
		}
	}

	return imageUrls, nil
}

// fetchNASAAssetManifest resolves the actual high-res image link from a NASA manifest.
func (c *NASAClient) fetchNASAAssetManifest(ctx context.Context, href string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", href, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var manifest []string
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return "", err
	}

	// Manifest contains various sizes. Look for ~orig.jpg or ~large.jpg
	var bestURL string
	for _, u := range manifest {
		if strings.HasSuffix(u, "~orig.jpg") {
			return u, nil
		}
		if strings.HasSuffix(u, "~large.jpg") {
			bestURL = u
		}
	}

	return bestURL, nil
}
