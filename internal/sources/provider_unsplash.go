package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	// unsplashReferral is the UTM attribution the Unsplash API guidelines
	// require on links back to Unsplash.
	unsplashReferral = "utm_source=frame_tv_art_manager&utm_medium=referral"
	unsplashLicense  = "Unsplash License"
)

// unsplashProvider handles communication with the Unsplash API and resolves artwork sources.
type unsplashProvider struct {
	accessKey string
	client    *http.Client
	gate      *rateGate
	logger    *slog.Logger
	BaseURL   string
}

// newUnsplashProvider creates a new Unsplash provider.
func newUnsplashProvider(accessKey string, logger *slog.Logger) *unsplashProvider {
	return &unsplashProvider{
		accessKey: accessKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		gate:    newRateGate(),
		logger:  logger,
		BaseURL: defaultUnsplashBaseURL,
	}
}

// unsplashPhoto represents the metadata returned by the Unsplash API.
type unsplashPhoto struct {
	ID             string `json:"id"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Description    string `json:"description"`
	AltDescription string `json:"alt_description"`
	User           struct {
		Name string `json:"name"`
	} `json:"user"`
	Links struct {
		HTML             string `json:"html"`
		DownloadLocation string `json:"download_location"`
	} `json:"links"`
	URLs struct {
		Raw string `json:"raw"`
	} `json:"urls"`
}

func (p *unsplashProvider) headers() map[string]string {
	return map[string]string{headerAuthorization: "Client-ID " + p.accessKey, "Accept-Version": "v1"}
}

// fetchJSON issues a Client-ID-authorized GET to apiURL and decodes the
// size-bounded JSON response body into out.
func (p *unsplashProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL, headers: p.headers(),
		maxBytes: unsplashMaxResponseBytes, statusLabel: "unsplash api", decodeLabel: "unsplash",
	}, out)
}

func (p *unsplashProvider) Name() string {
	return "unsplash"
}

func (p *unsplashProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "unsplash:")
}

// fetchCollectionPhotos retrieves all photos from a specific Unsplash collection using pagination.
func (p *unsplashProvider) fetchCollectionPhotos(ctx context.Context, collectionID string) ([]unsplashPhoto, error) {
	return collectPages(unsplashMaxPages, func(page int) ([]unsplashPhoto, bool, error) {
		apiURL := fmt.Sprintf("%s/collections/%s/photos?per_page=%d&page=%d", p.BaseURL, url.PathEscape(collectionID), unsplashPageSize, page)
		p.logger.Debug("fetching unsplash collection page", "id", collectionID, "page", page)
		var photos []unsplashPhoto
		err := p.fetchJSON(ctx, apiURL, &photos)
		return photos, len(photos) >= unsplashPageSize, err
	})
}

// TrackDownload reports a download to the Unsplash download endpoint, as the
// API guidelines require. Only endpoints on the configured API host are called.
func (p *unsplashProvider) TrackDownload(ctx context.Context, downloadLocation string) error {
	parsedURL, err := url.Parse(downloadLocation)
	if err != nil {
		return errors.New("invalid unsplash download location URL format")
	}
	baseURL, err := url.Parse(p.BaseURL)
	if err != nil || parsedURL.Host != baseURL.Host || parsedURL.Scheme != baseURL.Scheme {
		return errors.New("unsplash download location is not on the API host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadLocation, nil)
	if err != nil {
		return providerRequestError(ctx, "create unsplash download tracking request", err)
	}
	req.Header.Set("User-Agent", userAgent)
	for name, value := range p.headers() {
		req.Header.Set(name, value)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return providerRequestError(ctx, "track unsplash download", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, unsplashMaxResponseBytes))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unsplash download tracking error: %d", resp.StatusCode)
	}
	p.logger.Debug("unsplash download tracked", "url", truncateURL(downloadLocation))
	return nil
}

func (p *unsplashProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	if p.accessKey == "" {
		return nil, configError("UNSPLASH_ACCESS_KEY not configured")
	}
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 || parts[2] == "" {
		return nil, configError("invalid unsplash format")
	}

	var photos []unsplashPhoto
	switch parts[1] {
	case "collection":
		collection, err := p.fetchCollectionPhotos(ctx, parts[2])
		if err != nil {
			return nil, err
		}
		photos = collection
	case cmdPhoto:
		var photo unsplashPhoto
		if err := p.fetchJSON(ctx, fmt.Sprintf("%s/photos/%s", p.BaseURL, url.PathEscape(parts[2])), &photo); err != nil {
			return nil, err
		}
		photos = []unsplashPhoto{photo}
	default:
		return nil, configError("unknown unsplash type")
	}

	images := make([]sourceImage, 0, len(photos))
	for _, photo := range photos {
		image, err := p.image(parts[2], photo)
		if err != nil {
			p.logger.Warn("skipping unsplash photo without a downloadable image", "id", photo.ID, "error", err)
			continue
		}
		images = append(images, image)
	}
	if parts[1] == cmdPhoto && len(images) == 0 {
		return nil, errors.New("unsplash photo has no downloadable image")
	}
	return images, nil
}

// image requests a JPEG rendition that covers the panel without upscaling
// (fit=max) from the raw image URL.
func (p *unsplashProvider) image(source string, photo unsplashPhoto) (sourceImage, error) {
	if photo.ID == "" || photo.URLs.Raw == "" {
		return sourceImage{}, errors.New("missing photo ID or raw URL")
	}
	params := map[string]string{"fm": "jpg", "q": "95", "fit": "max"}
	maps.Copy(params, coverQuery(photo.Width, photo.Height))
	downloadURL, err := withQuery(photo.URLs.Raw, params)
	if err != nil {
		return sourceImage{}, err
	}
	sourceURL := photo.Links.HTML
	if sourceURL != "" {
		separator := "?"
		if strings.Contains(sourceURL, "?") {
			separator = "&"
		}
		sourceURL += separator + unsplashReferral
	}
	label := strings.ReplaceAll(filename(source+"-"+photo.ID), " ", "-")
	return sourceImage{
		URL: downloadURL, Identity: sourceIdentity(p.Name(), photo.ID, label), TrackURL: photo.Links.DownloadLocation,
		Attribution: attribution{
			Provider: p.Name(), Title: firstNonEmpty(photo.Description, photo.AltDescription),
			Artist: photo.User.Name, SourceURL: sourceURL, License: unsplashLicense,
		},
	}, nil
}
