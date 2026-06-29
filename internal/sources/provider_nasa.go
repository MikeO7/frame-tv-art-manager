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

const (
	// nasaMaxResponseBytes caps a decoded NASA API response to bound memory use.
	nasaMaxResponseBytes = 10 << 20 // 10 MiB
	// nasaMaxSearchItems limits image-library search results to avoid huge downloads.
	nasaMaxSearchItems = 10
)

// nasaProvider handles communication with NASA APIs and resolves artwork sources.
type nasaProvider struct {
	apiKey    string
	client    *http.Client
	logger    *slog.Logger
	BaseURL   string
	SearchURL string
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

// fetchJSON issues a GET to apiURL and decodes the size-bounded JSON response
// body into out, centralizing the NASA request boilerplate.
func (p *nasaProvider) fetchJSON(ctx context.Context, apiURL, errLabel string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s error: %d", errLabel, resp.StatusCode)
	}

	reader := http.MaxBytesReader(nil, resp.Body, nasaMaxResponseBytes)
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", errLabel, err)
	}
	return nil
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

	var apod apodResponse
	if err := p.fetchJSON(ctx, urlStr, "nasa apod api", &apod); err != nil {
		return nil, err
	}

	if apod.Type != mediaTypeImage {
		return nil, fmt.Errorf("today's apod is a %s, not an image", apod.Type)
	}

	return &apod, nil
}

// SearchNASAImageLibrary searches for high-resolution images in the NASA library.
func (p *nasaProvider) SearchNASAImageLibrary(ctx context.Context, query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/search?q=%s&media_type=image", p.SearchURL, url.QueryEscape(query))

	var result struct {
		Collection struct {
			Items []struct {
				Href string `json:"href"` // This is the manifest URL
				Data []struct {
					Title string `json:"title"`
				} `json:"data"`
			} `json:"items"`
		} `json:"collection"`
	}

	if err := p.fetchJSON(ctx, searchURL, "nasa image library", &result); err != nil {
		return nil, err
	}

	var imageUrls []string
	maxItems := min(len(result.Collection.Items), nasaMaxSearchItems)

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
	var manifest []string
	if err := p.fetchJSON(ctx, href, "nasa asset manifest api", &manifest); err != nil {
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
