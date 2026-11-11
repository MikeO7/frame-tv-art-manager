package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// pexelsMaxResponseBytes caps a decoded Pexels API response to guard against
	// unbounded memory use from a hostile or malfunctioning endpoint.
	pexelsMaxResponseBytes = 10 << 20 // 10 MiB
	// pexelsListSize is the item count requested for search and curated lists.
	pexelsListSize = 10
	// pexelsCollectionPageSize is the per-page item count requested for collections.
	pexelsCollectionPageSize = 80
	// pexelsMaxPages bounds collection pagination to avoid unbounded crawling.
	pexelsMaxPages = 10
	pexelsLicense  = "Pexels License"
)

// pexelsProvider handles communication with the Pexels API and resolves artwork sources.
type pexelsProvider struct {
	apiKey  string
	client  *http.Client
	gate    *rateGate
	logger  *slog.Logger
	BaseURL string
}

// newPexelsProvider creates a new Pexels provider.
func newPexelsProvider(apiKey string, logger *slog.Logger) *pexelsProvider {
	return &pexelsProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		gate:    newRateGate(),
		logger:  logger,
		BaseURL: "https://api.pexels.com",
	}
}

// pexelsPhoto represents the metadata returned by the Pexels API.
type pexelsPhoto struct {
	ID              int    `json:"id"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	URL             string `json:"url"`
	Alt             string `json:"alt"`
	Photographer    string `json:"photographer"`
	PhotographerURL string `json:"photographer_url"`
	Src             struct {
		Original string `json:"original"`
		Large2x  string `json:"large2x"`
	} `json:"src"`
}

// fetchJSON issues an authorized GET to apiURL and decodes the size-bounded
// JSON response body into out, centralizing the Pexels request boilerplate.
func (p *pexelsProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL,
		headers:  map[string]string{headerAuthorization: p.apiKey},
		maxBytes: pexelsMaxResponseBytes, statusLabel: "pexels api", decodeLabel: "pexels",
	}, out)
}

func (p *pexelsProvider) Name() string {
	return providerPexels
}

func (p *pexelsProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "pexels:")
}

// fetchCollection retrieves all photos from a specific Pexels collection using pagination.
func (p *pexelsProvider) fetchCollection(ctx context.Context, collectionID string) ([]pexelsPhoto, error) {
	return collectPages(pexelsMaxPages, func(page int) ([]pexelsPhoto, bool, error) {
		apiURL := fmt.Sprintf("%s/v1/collections/%s?type=photos&per_page=%d&page=%d",
			p.BaseURL, url.PathEscape(collectionID), pexelsCollectionPageSize, page)
		p.logger.Debug("fetching pexels collection page", "id", collectionID, "page", page)
		var result struct {
			Media []pexelsPhoto `json:"media"`
		}
		if err := p.fetchJSON(ctx, apiURL, &result); err != nil {
			return nil, false, err
		}
		return result.Media, len(result.Media) >= pexelsCollectionPageSize, nil
	})
}

func (p *pexelsProvider) fetchPhotoList(ctx context.Context, apiURL string) ([]pexelsPhoto, error) {
	var result struct {
		Photos []pexelsPhoto `json:"photos"`
	}
	if err := p.fetchJSON(ctx, apiURL, &result); err != nil {
		return nil, err
	}
	return result.Photos, nil
}

func (p *pexelsProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 {
		return nil, configError("invalid pexels format")
	}
	argument := ""
	if len(parts) == 3 {
		argument = parts[2]
	}

	var photos []pexelsPhoto
	var err error
	switch parts[1] {
	case cmdSearch:
		if argument == "" {
			return nil, configError("pexels search requires a query")
		}
		photos, err = p.fetchPhotoList(ctx, fmt.Sprintf("%s/v1/search?query=%s&per_page=%d", p.BaseURL, url.QueryEscape(argument), pexelsListSize))
	case "curated":
		photos, err = p.fetchPhotoList(ctx, fmt.Sprintf("%s/v1/curated?per_page=%d", p.BaseURL, pexelsListSize))
	case "collection":
		if argument == "" {
			return nil, configError("pexels collection requires an ID")
		}
		photos, err = p.fetchCollection(ctx, argument)
	case cmdPhoto:
		if argument == "" {
			return nil, configError("pexels photo requires an ID")
		}
		return p.resolvePhoto(ctx, argument)
	default:
		return nil, configError("unknown pexels type")
	}
	if err != nil {
		return nil, err
	}
	return p.images(photos), nil
}

// resolvePhoto retrieves a single photo by its ID.
func (p *pexelsProvider) resolvePhoto(ctx context.Context, photoID string) ([]sourceImage, error) {
	var photo pexelsPhoto
	if err := p.fetchJSON(ctx, fmt.Sprintf("%s/v1/photos/%s", p.BaseURL, url.PathEscape(photoID)), &photo); err != nil {
		return nil, err
	}
	image, ok := p.image(photo)
	if !ok {
		return nil, errors.New("pexels photo has no downloadable image")
	}
	return []sourceImage{image}, nil
}

// images keeps every photo with a usable original and skips the rest.
func (p *pexelsProvider) images(photos []pexelsPhoto) []sourceImage {
	images := make([]sourceImage, 0, len(photos))
	for _, photo := range photos {
		if image, ok := p.image(photo); ok {
			images = append(images, image)
		} else {
			p.logger.Warn("skipping pexels photo without a downloadable image", "id", photo.ID)
		}
	}
	return images
}

// image requests a CDN rendition that covers the panel instead of the
// original, which is frequently larger than the download and pixel caps.
// The identity stays derived from the original URL so it is independent of
// the rendition size.
func (p *pexelsProvider) image(photo pexelsPhoto) (sourceImage, bool) {
	if photo.Src.Original == "" {
		return sourceImage{}, false
	}
	params := map[string]string{"auto": "compress", "cs": "tinysrgb"}
	maps.Copy(params, coverQuery(photo.Width, photo.Height))
	primary, err := withQuery(photo.Src.Original, params)
	if err != nil {
		return sourceImage{}, false
	}
	image := sourceImage{
		URL: primary, Identity: sourceURLIdentity(p.Name(), photo.Src.Original),
		Attribution: attribution{
			Provider: p.Name(), Title: photo.Alt, Artist: photo.Photographer,
			SourceURL: photo.URL, License: pexelsLicense,
		},
	}
	if photo.Src.Large2x != "" {
		image.Fallbacks = []string{photo.Src.Large2x}
	}
	return image, true
}
