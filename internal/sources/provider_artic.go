package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// articMaxResponseBytes caps a decoded Art Institute API response to bound memory.
	articMaxResponseBytes = 10 << 20 // 10 MiB
	// articSearchLimit is the number of search results resolved per query.
	articSearchLimit = 10
	articFields      = "id,title,image_id,artist_display,is_public_domain,thumbnail"
	articLicense     = "CC0 1.0 (public domain)"
)

// articProvider handles communication with the Art Institute of Chicago API and resolves artwork sources.
type articProvider struct {
	client      *http.Client
	gate        *rateGate
	logger      *slog.Logger
	BaseURL     string
	IIIFBaseURL string
}

// newArticProvider creates a new Art Institute of Chicago provider.
func newArticProvider(logger *slog.Logger) *articProvider {
	return &articProvider{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		gate:        newRateGate(),
		logger:      logger,
		BaseURL:     "https://api.artic.edu",
		IIIFBaseURL: "https://www.artic.edu/iiif/2",
	}
}

// articArtwork represents an artwork returned by the Artic API. Thumbnail
// carries the full-resolution dimensions of the primary image.
type articArtwork struct {
	ID             int    `json:"id"`
	Title          string `json:"title"`
	ImageID        string `json:"image_id"`
	ArtistDisplay  string `json:"artist_display"`
	IsPublicDomain bool   `json:"is_public_domain"`
	Thumbnail      *struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"thumbnail"`
}

// fetchJSON issues a GET to apiURL and decodes the size-bounded JSON response
// body into out, centralizing request boilerplate.
func (p *articProvider) fetchJSON(ctx context.Context, apiURL, errLabel string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL,
		headers:  map[string]string{"AIC-User-Agent": userAgent},
		maxBytes: articMaxResponseBytes, statusLabel: "artic api", decodeLabel: errLabel,
	}, out)
}

func (p *articProvider) Name() string {
	return "artic"
}

func (p *articProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "artic:") || strings.HasPrefix(line, "art_institute:") || strings.HasPrefix(line, "art_institute_of_chicago:")
}

func (p *articProvider) Resolve(ctx context.Context, line string) ([]SourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 || parts[2] == "" {
		return nil, errors.New("invalid art_institute_of_chicago format")
	}
	switch parts[1] {
	case cmdSearch:
		return p.search(ctx, parts[2])
	case cmdPhoto:
		return p.resolvePhoto(ctx, parts[2])
	default:
		return nil, errors.New("unknown artic type")
	}
}

// search resolves public-domain matches only: other works are served at a
// copyright-restricted size far below the panel resolution.
func (p *articProvider) search(ctx context.Context, query string) ([]SourceImage, error) {
	params := url.Values{
		"q": {query}, "fields": {articFields}, "limit": {strconv.Itoa(articSearchLimit)},
		"query[term][is_public_domain]": {"true"},
	}
	var result struct {
		Data []articArtwork `json:"data"`
	}
	if err := p.fetchJSON(ctx, p.BaseURL+"/api/v1/artworks/search?"+params.Encode(), "artic search", &result); err != nil {
		return nil, err
	}
	images := make([]SourceImage, 0, len(result.Data))
	for _, artwork := range result.Data {
		if image, ok := p.image(artwork); ok {
			images = append(images, image)
		} else {
			p.logger.Warn("skipping art institute artwork without a public-domain image", "id", artwork.ID)
		}
	}
	return images, nil
}

// resolvePhoto retrieves a single artwork by its ID.
func (p *articProvider) resolvePhoto(ctx context.Context, id string) ([]SourceImage, error) {
	apiURL := fmt.Sprintf("%s/api/v1/artworks/%s?fields=%s", p.BaseURL, url.PathEscape(id), articFields)
	var result struct {
		Data articArtwork `json:"data"`
	}
	if err := p.fetchJSON(ctx, apiURL, "artic", &result); err != nil {
		return nil, err
	}
	if result.Data.ImageID == "" {
		return nil, errors.New("artwork has no image_id")
	}
	if !result.Data.IsPublicDomain {
		return nil, errors.New("artwork is not in the public domain, so its image is only available at a restricted size")
	}
	image, ok := p.image(result.Data)
	if !ok {
		return nil, errors.New("artwork image is too small for an allowed IIIF size")
	}
	return []SourceImage{image}, nil
}

func (p *articProvider) image(artwork articArtwork) (SourceImage, bool) {
	if artwork.ImageID == "" || !artwork.IsPublicDomain {
		return SourceImage{}, false
	}
	var width, height int
	if artwork.Thumbnail != nil {
		width, height = artwork.Thumbnail.Width, artwork.Thumbnail.Height
	}
	candidates := p.imageCandidates(artwork.ImageID, width, height)
	if len(candidates) == 0 {
		return SourceImage{}, false
	}
	// The identity keeps the original rendition URL as its stable key so
	// artwork resolved before size selection is recognized.
	legacy := fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", p.IIIFBaseURL, artwork.ImageID)
	return SourceImage{
		URL: candidates[0], Fallbacks: candidates[1:], Identity: sourceURLIdentity(p.Name(), legacy),
		Attribution: Attribution{
			Provider: p.Name(), Title: artwork.Title, Artist: strings.Join(strings.Fields(artwork.ArtistDisplay), " "),
			SourceURL: "https://www.artic.edu/artworks/" + strconv.Itoa(artwork.ID), License: articLicense,
		},
	}, true
}

// imageCandidates lists IIIF renditions from largest to smallest. The Art
// Institute serves public-domain images only at a fixed set of edge lengths
// (843, 1200, and 1686 are documented; 3000 is served as well), never above
// the source size. The edge that limits covering the panel is requested, so
// portraits are sized by width and panoramas by height, and renditions that
// would exceed the download pixel cap are left out.
func (p *articProvider) imageCandidates(imageID string, width, height int) []string {
	axis, _ := coverRendition(width, height)
	edge, other := width, height
	if axis == axisHeight {
		edge, other = height, width
	}
	var candidates []string
	for _, size := range [...]int{3000, 1686, 1200, 843} {
		if edge > 0 && other > 0 && (size > edge || int64(size)*int64(size)*int64(other)/int64(edge) > maxDownloadedPixels) {
			continue
		}
		region := strconv.Itoa(size) + ","
		if axis == axisHeight {
			region = "," + strconv.Itoa(size)
		}
		candidates = append(candidates, fmt.Sprintf("%s/%s/full/%s/0/default.jpg", p.IIIFBaseURL, imageID, region))
	}
	return candidates
}
