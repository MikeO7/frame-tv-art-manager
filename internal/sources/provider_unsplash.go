package sources

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultUnsplashBaseURL = "https://api.unsplash.com"

	// unsplashMaxResponseBytes caps a decoded Unsplash API response to bound memory.
	unsplashMaxResponseBytes = 10 << 20 // 10 MiB
	// unsplashPageSize is the per-page item count requested for collections.
	unsplashPageSize = 30
	// unsplashMaxPages bounds collection pagination to avoid unbounded crawling.
	unsplashMaxPages = 33
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
		BaseURL: defaultUnsplashBaseURL,
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

// fetchJSON issues a Client-ID-authorized GET to apiURL and decodes the
// size-bounded JSON response body into out.
func (p *unsplashProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, url: apiURL, headers: map[string]string{headerAuthorization: "Client-ID " + p.accessKey},
		maxBytes: unsplashMaxResponseBytes, statusLabel: "unsplash api", decodeLabel: "unsplash",
	}, out)
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
		apiURL := fmt.Sprintf("%s/collections/%s/photos?per_page=%d&page=%d", p.BaseURL, url.PathEscape(collectionID), unsplashPageSize, page)
		p.logger.Debug("fetching unsplash collection page", "id", collectionID, "page", page)

		var pagePhotos []unsplashPhoto
		if err := p.fetchJSON(ctx, apiURL, &pagePhotos); err != nil {
			return nil, err
		}

		if len(pagePhotos) == 0 {
			break
		}

		allPhotos = append(allPhotos, pagePhotos...)

		if len(pagePhotos) < unsplashPageSize {
			break
		}
		page++

		if page > unsplashMaxPages {
			break
		}
	}

	return allPhotos, nil
}

// FetchPhoto retrieves metadata for a single Unsplash photo.
func (p *unsplashProvider) FetchPhoto(ctx context.Context, photoID string) (*unsplashPhoto, error) {
	apiURL := fmt.Sprintf("%s/photos/%s", p.BaseURL, url.PathEscape(photoID))

	var photo unsplashPhoto
	if err := p.fetchJSON(ctx, apiURL, &photo); err != nil {
		return nil, err
	}

	return &photo, nil
}

// TrackDownload triggers the Unsplash "download" endpoint for a photo.
func (p *unsplashProvider) TrackDownload(ctx context.Context, downloadLocation string) {
	parsedURL, err := url.Parse(downloadLocation)
	if err != nil {
		p.logger.Warn("invalid unsplash download location URL format", "url", truncateURL(downloadLocation))
		return
	}
	baseURL, err := url.Parse(p.BaseURL)
	if err != nil || parsedURL.Host != baseURL.Host || parsedURL.Scheme != baseURL.Scheme {
		p.logger.Warn("invalid unsplash download location host or scheme", "url", truncateURL(downloadLocation))
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
		p.logger.Debug("unsplash download tracked", "url", truncateURL(downloadLocation))
	}
}

func (p *unsplashProvider) Resolve(ctx context.Context, line string) ([]SourceImage, error) {
	if p.accessKey == "" {
		return nil, fmt.Errorf("UNSPLASH_ACCESS_KEY not configured")
	}

	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid unsplash format")
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
		return nil, fmt.Errorf("unknown unsplash type")
	}

	if err != nil {
		return nil, err
	}

	images := make([]SourceImage, 0, len(photos))
	for _, ph := range photos {
		phCopy := ph
		urlStr := phCopy.URLs.Raw + "&w=3840&q=95&fm=jpg"

		label := strings.ReplaceAll(Filename(parts[2]+"-"+phCopy.ID), " ", "-")
		identity := sourceIdentity(p.Name(), phCopy.ID, label)

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
