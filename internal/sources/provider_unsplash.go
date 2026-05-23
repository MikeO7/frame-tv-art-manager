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

// unsplashProvider handles communication with the Unsplash API and resolves artwork sources.
type unsplashProvider struct {
	appID     string
	accessKey string
	secretKey string
	client    *http.Client
	logger    *slog.Logger
	BaseURL   string
}

// unsplashClient is a type alias for unsplashProvider to maintain backwards compatibility in tests.
type unsplashClient = unsplashProvider

// newUnsplashClient creates a new Unsplash API client/provider.
//
//nolint:unparam
func newUnsplashClient(appID, accessKey, secretKey string, logger *slog.Logger) *unsplashClient {
	return newUnsplashProvider(appID, accessKey, secretKey, logger)
}

// newUnsplashProvider creates a new Unsplash provider.
func newUnsplashProvider(appID, accessKey, secretKey string, logger *slog.Logger) *unsplashProvider {
	return &unsplashProvider{
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

// unsplashPhoto represents the metadata returned by the Unsplash API.
type unsplashPhoto struct {
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

func (p *unsplashProvider) Name() string {
	return "unsplash"
}

func (p *unsplashProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "unsplash:")
}

// FetchCollectionPhotos retrieves all photos from a specific Unsplash collection using pagination.
func (p *unsplashProvider) FetchCollectionPhotos(ctx context.Context, collectionID string) ([]unsplashPhoto, error) {
	var allPhotos []unsplashPhoto
	page := 1

	for {
		apiURL := fmt.Sprintf("%s/collections/%s/photos?per_page=30&page=%d", p.BaseURL, url.PathEscape(collectionID), page)
		p.logger.Debug("fetching unsplash collection page", "id", collectionID, "page", page)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Client-ID "+p.accessKey)

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("unsplash api error: %d", resp.StatusCode)
		}

		var pagePhotos []unsplashPhoto
		maxBytes := int64(10 * 1024 * 1024) // 10MB limit
		reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
		decodeErr := json.NewDecoder(reader).Decode(&pagePhotos)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode unsplash response: %w", decodeErr)
		}

		if len(pagePhotos) == 0 {
			break
		}

		allPhotos = append(allPhotos, pagePhotos...)

		if len(pagePhotos) < 30 {
			break
		}
		page++

		if page > 33 {
			break
		}
	}

	return allPhotos, nil
}

// FetchPhoto retrieves metadata for a single Unsplash photo.
func (p *unsplashProvider) FetchPhoto(ctx context.Context, photoID string) (*unsplashPhoto, error) {
	apiURL := fmt.Sprintf("%s/photos/%s", p.BaseURL, url.PathEscape(photoID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Client-ID "+p.accessKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsplash api error: %d", resp.StatusCode)
	}

	var photo unsplashPhoto
	maxBytes := int64(10 * 1024 * 1024) // 10MB limit
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)
	if err := json.NewDecoder(reader).Decode(&photo); err != nil {
		return nil, fmt.Errorf("decode unsplash response: %w", err)
	}

	return &photo, nil
}

// TrackDownload triggers the Unsplash "download" endpoint for a photo.
func (p *unsplashProvider) TrackDownload(ctx context.Context, downloadLocation string) {
	parsedURL, err := url.Parse(downloadLocation)
	if err != nil {
		p.logger.Warn("invalid unsplash download location URL format", "url", downloadLocation)
		return
	}
	baseURL, err := url.Parse(p.BaseURL)
	if err != nil || parsedURL.Host != baseURL.Host {
		p.logger.Warn("invalid unsplash download location host", "url", downloadLocation)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadLocation, nil)
	if err != nil {
		return
	}

	req.Header.Set("Authorization", "Client-ID "+p.accessKey)

	resp, err := p.client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		p.logger.Debug("unsplash download tracked", "url", downloadLocation)
	}
}

func (p *unsplashProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	if p.accessKey == "" {
		return nil, fmt.Errorf("UNSPLASH_ACCESS_KEY not configured")
	}

	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid unsplash format: %s", line)
	}

	var photos []unsplashPhoto
	var err error

	switch parts[1] {
	case "collection":
		photos, err = p.FetchCollectionPhotos(ctx, parts[2])
	case cmdPhoto:
		photo, fetchErr := p.FetchPhoto(ctx, parts[2])
		if fetchErr == nil {
			photos = []unsplashPhoto{*photo}
		}
		err = fetchErr
	default:
		return nil, fmt.Errorf("unknown unsplash type: %s", parts[1])
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(photos))
	for _, ph := range photos {
		phCopy := ph
		urlStr := phCopy.URLs.Raw + "&w=3840&q=95&fm=jpg"

		slug := Filename(parts[2] + "-" + phCopy.ID)
		slug = strings.ReplaceAll(slug, " ", "-")
		if len(slug) > 100 {
			slug = slug[:100]
		}
		idx := atomic.AddInt32(globalIndex, 1) - 1
		identity := fmt.Sprintf("%03d__unsplash__%s", idx, slug)

		images = append(images, SourceImage{
			URL:      urlStr,
			Identity: identity,
			OnDownload: func(ctx context.Context) error {
				p.TrackDownload(ctx, phCopy.Links.DownloadLocation)
				return nil
			},
		})
	}

	return images, nil
}
