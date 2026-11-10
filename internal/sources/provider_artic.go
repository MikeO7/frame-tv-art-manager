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
	articFields           = "id,title,image_id,artist_display,date_display,is_public_domain,thumbnail"
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
	DateDisplay    string `json:"date_display"`
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

func (p *articProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
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
func (p *articProvider) search(ctx context.Context, query string) ([]sourceImage, error) {
	candidates, err := p.searchCandidates(ctx, query, museumSearchLimit)
	if err != nil {
		return nil, err
	}
	return candidateImages(candidates), nil
}

func (p *articProvider) searchCandidates(ctx context.Context, query string, limit int) ([]Candidate, error) {
	params := url.Values{
		"q": {query}, "fields": {articFields}, paramLimit: {strconv.Itoa(searchLimit(limit))},
		"query[term][is_public_domain]": {"true"},
	}
	var result struct {
		Data []articArtwork `json:"data"`
	}
	if err := p.fetchJSON(ctx, p.BaseURL+"/api/v1/artworks/search?"+params.Encode(), "artic search", &result); err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(result.Data))
	for _, artwork := range result.Data {
		if candidate, ok := p.candidate(artwork); ok {
			candidates = append(candidates, candidate)
		} else {
			p.logger.Warn("skipping art institute artwork without a public-domain image", "id", artwork.ID)
		}
	}
	return candidates, nil
}

// resolvePhoto retrieves a single artwork by its ID.
func (p *articProvider) resolvePhoto(ctx context.Context, id string) ([]sourceImage, error) {
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
	candidate, ok := p.candidate(result.Data)
	if !ok {
		return nil, errors.New("artwork image is too small for an allowed IIIF size")
	}
	return []sourceImage{candidate.image}, nil
}

func (p *articProvider) candidate(artwork articArtwork) (Candidate, bool) {
	if artwork.ImageID == "" || !artwork.IsPublicDomain {
		return Candidate{}, false
	}
	var width, height int
	if artwork.Thumbnail != nil {
		width, height = artwork.Thumbnail.Width, artwork.Thumbnail.Height
	}
	candidates := p.imageCandidates(artwork.ImageID, width, height)
	if len(candidates) == 0 {
		return Candidate{}, false
	}
	// The identity keeps the original rendition URL as its stable key so
	// artwork resolved before size selection is recognized.
	legacy := fmt.Sprintf("%s/%s/full/!3840,2160/0/default.jpg", p.IIIFBaseURL, artwork.ImageID)
	id := strconv.Itoa(artwork.ID)
	attribution := attribution{
		Provider: p.Name(), Title: artwork.Title, Artist: strings.Join(strings.Fields(artwork.ArtistDisplay), " "),
		SourceURL: "https://www.artic.edu/artworks/" + id, License: publicDomainCC0,
	}
	renditionWidth, renditionHeight := articRenditionSize(candidates[0], width, height)
	return Candidate{
		Provider: p.Name(), ID: id, Title: attribution.Title, Artist: attribution.Artist, Date: plainText(artwork.DateDisplay),
		ImageURL: candidates[0], Width: renditionWidth, Height: renditionHeight, License: publicDomainCC0,
		Link: attribution.SourceURL, SourceLine: "artic:" + cmdPhoto + ":" + id,
		image: sourceImage{
			URL: candidates[0], Fallbacks: candidates[1:], Identity: sourceURLIdentity(p.Name(), legacy), Attribution: attribution,
		},
	}, true
}

// articRenditionSize returns the pixel size of an IIIF rendition URL whose
// size segment is "w," or ",h", or zeros when the source size is unknown.
func articRenditionSize(renditionURL string, width, height int) (int, int) {
	segments := strings.Split(renditionURL, "/")
	if len(segments) < 3 {
		return 0, 0
	}
	widthText, heightText, _ := strings.Cut(segments[len(segments)-3], ",")
	if size, err := strconv.Atoi(widthText); err == nil {
		return scaledSize(width, height, axisWidth, size)
	}
	if size, err := strconv.Atoi(heightText); err == nil {
		return scaledSize(width, height, axisHeight, size)
	}
	return 0, 0
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
