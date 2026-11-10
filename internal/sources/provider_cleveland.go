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
	providerCleveland = "cleveland"
	// clevelandMaxResponseBytes caps a decoded Cleveland API response.
	clevelandMaxResponseBytes = 10 << 20 // 10 MiB
	clevelandFields           = "id,accession_number,title,creation_date,creators,url,images,share_license_status"
	clevelandCC0              = "CC0"
)

// clevelandProvider resolves CC0 works from the Cleveland Museum of Art Open
// Access API, which needs no key.
type clevelandProvider struct {
	client  *http.Client
	gate    *rateGate
	logger  *slog.Logger
	BaseURL string
}

func newClevelandProvider(logger *slog.Logger) *clevelandProvider {
	return &clevelandProvider{
		client:  &http.Client{Timeout: 30 * time.Second},
		gate:    newRateGate(),
		logger:  logger,
		BaseURL: "https://openaccess-api.clevelandart.org",
	}
}

// clevelandImage is one rendition; the API reports dimensions as strings.
type clevelandImage struct {
	URL    string `json:"url"`
	Width  string `json:"width"`
	Height string `json:"height"`
}

type clevelandArtwork struct {
	ID                 int    `json:"id"`
	Title              string `json:"title"`
	CreationDate       string `json:"creation_date"`
	URL                string `json:"url"`
	ShareLicenseStatus string `json:"share_license_status"`
	Creators           []struct {
		Description string `json:"description"`
	} `json:"creators"`
	Images struct {
		Web   *clevelandImage `json:"web"`
		Print *clevelandImage `json:"print"`
		Full  *clevelandImage `json:"full"`
	} `json:"images"`
}

func (p *clevelandProvider) Name() string {
	return providerCleveland
}

func (p *clevelandProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, providerCleveland+":")
}

func (p *clevelandProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL,
		maxBytes: clevelandMaxResponseBytes, statusLabel: "cleveland api", decodeLabel: "cleveland",
	}, out)
}

func (p *clevelandProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 || parts[2] == "" {
		return nil, errors.New("invalid cleveland format; use cleveland:search:<query> or cleveland:object:<id>")
	}
	switch parts[1] {
	case cmdSearch:
		query, filters, err := searchArgument(parts[2],
			"type", "department", "highlight", "artists", "title", "created_after", "created_before", "currently_on_view")
		if err != nil {
			return nil, fmt.Errorf("cleveland search: %w", err)
		}
		candidates, err := p.search(ctx, query, filters, museumSearchLimit)
		if err != nil {
			return nil, err
		}
		return candidateImages(candidates), nil
	case cmdObject:
		var result struct {
			Data clevelandArtwork `json:"data"`
		}
		apiURL := fmt.Sprintf("%s/api/artworks/%s?fields=%s", p.BaseURL, url.PathEscape(parts[2]), clevelandFields)
		if err := p.fetchJSON(ctx, apiURL, &result); err != nil {
			return nil, err
		}
		candidate, ok := p.candidate(result.Data)
		if !ok {
			return nil, errors.New("cleveland artwork is not CC0 or has no JPEG image large enough for the panel")
		}
		return []sourceImage{candidate.image}, nil
	default:
		return nil, errors.New("unknown cleveland type")
	}
}

func (p *clevelandProvider) searchCandidates(ctx context.Context, query string, limit int) ([]Candidate, error) {
	return p.search(ctx, query, url.Values{}, limit)
}

// search asks only for CC0 works with images, requesting extra hits because
// some have no rendition large enough for the panel.
func (p *clevelandProvider) search(ctx context.Context, query string, filters url.Values, limit int) ([]Candidate, error) {
	limit = searchLimit(limit)
	params := url.Values{"cc0": {"1"}, "has_image": {"1"}, paramLimit: {strconv.Itoa(limit * 2)}, "fields": {clevelandFields}}
	for name, values := range filters {
		params[name] = values
	}
	if query != "" {
		params.Set("q", query)
	}
	var result struct {
		Data []clevelandArtwork `json:"data"`
	}
	if err := p.fetchJSON(ctx, p.BaseURL+"/api/artworks/?"+params.Encode(), &result); err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, limit)
	for _, artwork := range result.Data {
		if len(candidates) == limit {
			break
		}
		if candidate, ok := p.candidate(artwork); ok {
			candidates = append(candidates, candidate)
		} else {
			p.logger.Debug("skipping cleveland artwork without a usable CC0 image", "id", artwork.ID)
		}
	}
	return candidates, nil
}

// candidate prefers the print JPEG (about 3400 pixels on the long edge) over
// the web rendition, which is too small; the full rendition is a TIFF of
// several hundred megabytes and is used only if the API ever serves a JPEG.
func (p *clevelandProvider) candidate(artwork clevelandArtwork) (Candidate, bool) {
	if artwork.ShareLicenseStatus != clevelandCC0 || artwork.ID <= 0 {
		return Candidate{}, false
	}
	var renditions []rendition
	for _, image := range []*clevelandImage{artwork.Images.Full, artwork.Images.Print, artwork.Images.Web} {
		if image == nil || image.URL == "" {
			continue
		}
		width, widthErr := strconv.Atoi(image.Width)
		height, heightErr := strconv.Atoi(image.Height)
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			continue
		}
		renditions = append(renditions, rendition{url: image.URL, width: width, height: height})
	}
	ranked := rankRenditions(renditions)
	if len(ranked) == 0 {
		return Candidate{}, false
	}
	id := strconv.Itoa(artwork.ID)
	link := artwork.URL
	if link == "" {
		link = "https://clevelandart.org/art/" + id
	}
	var artist string
	if len(artwork.Creators) > 0 {
		artist = plainText(artwork.Creators[0].Description)
	}
	urls := renditionURLs(ranked)
	attribution := attribution{Provider: p.Name(), Title: plainText(artwork.Title), Artist: artist, SourceURL: link, License: publicDomainCC0}
	return Candidate{
		Provider: p.Name(), ID: id, Title: attribution.Title, Artist: artist, Date: plainText(artwork.CreationDate),
		ImageURL: ranked[0].url, Width: ranked[0].width, Height: ranked[0].height, License: publicDomainCC0, Link: link,
		SourceLine: providerCleveland + ":" + cmdObject + ":" + id,
		image: sourceImage{
			URL: urls[0], Fallbacks: urls[1:], Identity: museumObjectIdentity(p.Name(), id), Attribution: attribution,
		},
	}, true
}
