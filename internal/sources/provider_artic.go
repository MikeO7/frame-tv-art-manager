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

// articProvider handles communication with the Art Institute of Chicago API and resolves artwork sources.
type articProvider struct {
	client      *http.Client
	logger      *slog.Logger
	BaseURL     string
	IIIFBaseURL string
}

// articClient is a type alias for articProvider to maintain backwards compatibility in tests.
type articClient = articProvider

// newArticClient creates a new Art Institute of Chicago API client/provider.
func newArticClient(logger *slog.Logger) *articClient {
	return newArticProvider(logger)
}

// newArticProvider creates a new Art Institute of Chicago provider.
func newArticProvider(logger *slog.Logger) *articProvider {
	return &articProvider{
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

func (p *articProvider) Name() string {
	return "artic"
}

func (p *articProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "artic:") || strings.HasPrefix(line, "art_institute:") || strings.HasPrefix(line, "art_institute_of_chicago:")
}

// Search Masterpieces from the Artic library.
func (p *articProvider) Search(ctx context.Context, query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/api/v1/artworks/search?q=%s&fields=id,title,image_id&limit=10", p.BaseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "FrameTVArtManager/1.0 (https://github.com/MikeO7/frame-tv-art-manager)")

	resp, err := p.client.Do(req)
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
			imgURL := fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", p.IIIFBaseURL, art.ImageID)
			imageUrls = append(imageUrls, imgURL)
		}
	}

	return imageUrls, nil
}

// FetchPhoto retrieves a single masterpiece by its ID.
func (p *articProvider) FetchPhoto(ctx context.Context, id string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/v1/artworks/%s?fields=id,title,image_id", p.BaseURL, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "FrameTVArtManager/1.0 (https://github.com/MikeO7/frame-tv-art-manager)")

	resp, err := p.client.Do(req)
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

	return fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", p.IIIFBaseURL, result.Data.ImageID), nil
}

func (p *articProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid art_institute_of_chicago format: %s", line)
	}

	var urls []string
	var err error

	switch parts[1] {
	case cmdSearch:
		urls, err = p.Search(ctx, parts[2])
	case cmdPhoto:
		urlStr, fetchErr := p.FetchPhoto(ctx, parts[2])
		if fetchErr == nil {
			urls = []string{urlStr}
		}
		err = fetchErr
	default:
		return nil, fmt.Errorf("unknown artic type: %s", parts[1])
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(urls))
	for _, u := range urls {
		slug := URLToSlug(u)
		if strings.Contains(u, "artic.edu") {
			urlParts := strings.Split(u, "/")
			if len(urlParts) > 5 {
				slug = Filename(urlParts[5])
				if len(slug) > 100 {
					slug = slug[:100]
				}
			}
		}

		idx := atomic.AddInt32(globalIndex, 1) - 1
		identity := fmt.Sprintf("%03d__artic__%s", idx, slug)

		images = append(images, SourceImage{
			URL:      u,
			Identity: identity,
		})
	}

	return images, nil
}
