package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// nasaProvider handles communication with NASA APIs and resolves artwork sources.
type nasaProvider struct {
	apiKey    string
	client    *http.Client
	logger    *slog.Logger
	BaseURL   string
	SearchURL string
}

// nasaClient is a type alias for nasaProvider to maintain backwards compatibility in tests.
type nasaClient = nasaProvider

// newNASAClient creates a new NASA API client/provider.
func newNASAClient(apiKey string, logger *slog.Logger) *nasaClient {
	return newNasaProvider(apiKey, logger)
}

// newNasaProvider creates a new NASA provider.
func newNasaProvider(apiKey string, logger *slog.Logger) *nasaProvider {
	if apiKey == "" {
		apiKey = "DEMO_KEY"
	}
	return &nasaProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:    logger,
		BaseURL:   "https://api.nasa.gov",
		SearchURL: "https://images-api.nasa.gov",
	}
}

// apodResponse represents the response from NASA's APOD API.
type apodResponse struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	HDURL string `json:"hdurl"`
	Type  string `json:"media_type"`
}

func (p *nasaProvider) Name() string {
	return providerNASA
}

func (p *nasaProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "nasa:")
}

// FetchAPOD retrieves today's Astronomy Picture of the Day.
func (p *nasaProvider) FetchAPOD(ctx context.Context) (*apodResponse, error) {
	urlStr := fmt.Sprintf("%s/planetary/apod?api_key=%s", p.BaseURL, p.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nasa apod api error: %d", resp.StatusCode)
	}

	var apod apodResponse
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&apod); err != nil {
		return nil, fmt.Errorf("decode nasa apod response: %w", err)
	}

	if apod.Type != mediaTypeImage {
		return nil, fmt.Errorf("today's apod is a %s, not an image", apod.Type)
	}

	return &apod, nil
}

// SearchNASAImageLibrary searches for high-resolution images in the NASA library.
func (p *nasaProvider) SearchNASAImageLibrary(ctx context.Context, query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/search?q=%s&media_type=image", p.SearchURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
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

	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
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
		manifestURL, err := p.fetchNASAAssetManifest(ctx, item.Href)
		if err != nil {
			p.logger.Warn("failed to fetch nasa asset manifest", "href", item.Href, "error", err)
			continue
		}
		if manifestURL != "" {
			imageUrls = append(imageUrls, manifestURL)
		}
	}

	return imageUrls, nil
}

// fetchNASAAssetManifest resolves the actual high-res image link from a NASA manifest.
func (p *nasaProvider) fetchNASAAssetManifest(ctx context.Context, href string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return "", err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nasa asset manifest api error: %d", resp.StatusCode)
	}

	var manifest []string
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		return "", err
	}

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

func (p *nasaProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid nasa format: %s", line)
	}

	var urls []string
	var err error

	switch parts[1] {
	case "apod":
		apod, apodErr := p.FetchAPOD(ctx)
		if apodErr != nil {
			return nil, apodErr
		}
		u := apod.HDURL
		if u == "" {
			u = apod.URL
		}
		if u != "" {
			urls = append(urls, u)
		}
	case cmdSearch:
		if len(parts) < 3 {
			return nil, fmt.Errorf("nasa search requires a query: nasa:search:query")
		}
		urls, err = p.SearchNASAImageLibrary(ctx, parts[2])
	default:
		return nil, fmt.Errorf("unknown nasa type: %s", parts[1])
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(urls))
	for _, u := range urls {
		slug := slugFromNASAURL(u)

		idx := atomic.AddInt32(globalIndex, 1) - 1
		identity := fmt.Sprintf("%03d__nasa__%s", idx, slug)

		images = append(images, SourceImage{
			URL:      u,
			Identity: identity,
		})
	}

	return images, nil
}
