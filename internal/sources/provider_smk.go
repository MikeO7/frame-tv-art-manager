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
	providerSMK = "smk"
	// smkMaxResponseBytes caps a decoded SMK API response; records carry long
	// artist biographies.
	smkMaxResponseBytes = 10 << 20 // 10 MiB
	// smkRequestInterval keeps SMK, which publishes no rate limit, at a
	// gentle pace.
	smkRequestInterval = 250 * time.Millisecond
	smkLicense         = "Public Domain Mark 1.0"
)

// smkProvider resolves public-domain works from SMK, the National Gallery of
// Denmark, whose open API needs no key. Images come from its IIIF server.
type smkProvider struct {
	client  *http.Client
	gate    *rateGate
	logger  *slog.Logger
	BaseURL string
}

func newSMKProvider(logger *slog.Logger) *smkProvider {
	return &smkProvider{
		client:  &http.Client{Timeout: 30 * time.Second},
		gate:    newPacedRateGate(smkRequestInterval),
		logger:  logger,
		BaseURL: "https://api.smk.dk/api/v1",
	}
}

type smkArtwork struct {
	ObjectNumber string `json:"object_number"`
	PublicDomain bool   `json:"public_domain"`
	HasImage     bool   `json:"has_image"`
	IIIFID       string `json:"image_iiif_id"`
	Native       string `json:"image_native"`
	Width        int    `json:"image_width"`
	Height       int    `json:"image_height"`
	FrontendURL  string `json:"frontend_url"`
	Titles       []struct {
		Title string `json:"title"`
	} `json:"titles"`
	Artist         []string `json:"artist"`
	ProductionDate []struct {
		Period string `json:"period"`
	} `json:"production_date"`
}

type smkResponse struct {
	Items []smkArtwork `json:"items"`
}

func (p *smkProvider) Name() string {
	return providerSMK
}

func (p *smkProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, providerSMK+":")
}

func (p *smkProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL,
		maxBytes: smkMaxResponseBytes, statusLabel: "smk api", decodeLabel: "smk",
	}, out)
}

func (p *smkProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 || parts[2] == "" {
		return nil, errors.New("invalid smk format; use smk:search:<query> or smk:object:<object number>")
	}
	switch parts[1] {
	case cmdSearch:
		query, _, err := searchArgument(parts[2])
		if err != nil {
			return nil, fmt.Errorf("smk search: %w", err)
		}
		candidates, err := p.searchCandidates(ctx, query, museumSearchLimit)
		if err != nil {
			return nil, err
		}
		return candidateImages(candidates), nil
	case cmdObject:
		params := url.Values{"object_number": {parts[2]}, "lang": {"en"}}
		var result smkResponse
		if err := p.fetchJSON(ctx, p.BaseURL+"/art/?"+params.Encode(), &result); err != nil {
			return nil, err
		}
		if len(result.Items) == 0 {
			return nil, errors.New("smk object not found")
		}
		candidate, ok := p.candidate(result.Items[0])
		if !ok {
			return nil, errors.New("smk object is not public domain or has no image large enough for the panel")
		}
		return []sourceImage{candidate.image}, nil
	default:
		return nil, errors.New("unknown smk type")
	}
}

// searchCandidates asks only for public-domain works with images, requesting
// extra hits because some images are too small for the panel.
func (p *smkProvider) searchCandidates(ctx context.Context, query string, limit int) ([]Candidate, error) {
	limit = searchLimit(limit)
	params := url.Values{
		"keys": {query}, "filters": {"[public_domain:true],[has_image:true]"},
		"offset": {"0"}, "rows": {strconv.Itoa(limit * 2)}, "lang": {"en"},
	}
	var result smkResponse
	if err := p.fetchJSON(ctx, p.BaseURL+"/art/search/?"+params.Encode(), &result); err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, limit)
	for _, artwork := range result.Items {
		if len(candidates) == limit {
			break
		}
		if candidate, ok := p.candidate(artwork); ok {
			candidates = append(candidates, candidate)
		} else {
			p.logger.Debug("skipping smk artwork without a usable public-domain image", "object_number", artwork.ObjectNumber)
		}
	}
	return candidates, nil
}

// candidate requests an IIIF rendition that covers the panel (never larger
// than the scan), with a half-size IIIF rendition and the full-size download
// as fallbacks when they fit the pixel cap.
func (p *smkProvider) candidate(artwork smkArtwork) (Candidate, bool) {
	if !artwork.PublicDomain || artwork.ObjectNumber == "" || artwork.Width <= 0 || artwork.Height <= 0 {
		return Candidate{}, false
	}
	var renditions []rendition
	if base := strings.TrimSuffix(artwork.IIIFID, "/"); strings.HasPrefix(base, schemeHTTPS+"://") {
		axis, size := coverRendition(artwork.Width, artwork.Height)
		edge := artwork.Width
		if axis == axisHeight {
			edge = artwork.Height
		}
		for _, length := range []int{min(size, edge), min(size, edge) / 2} {
			width, height := scaledSize(artwork.Width, artwork.Height, axis, length)
			region := strconv.Itoa(length) + ","
			if axis == axisHeight {
				region = "," + strconv.Itoa(length)
			}
			renditions = append(renditions, rendition{url: base + "/full/" + region + "/0/default.jpg", width: width, height: height})
		}
	}
	if artwork.Native != "" {
		renditions = append(renditions, rendition{url: artwork.Native, width: artwork.Width, height: artwork.Height})
	}
	ranked := rankRenditions(renditions)
	if len(ranked) == 0 {
		return Candidate{}, false
	}
	var title, date, artist string
	if len(artwork.Titles) > 0 {
		title = plainText(artwork.Titles[0].Title)
	}
	if len(artwork.ProductionDate) > 0 {
		date = plainText(artwork.ProductionDate[0].Period)
	}
	if len(artwork.Artist) > 0 {
		artist = plainText(strings.Join(artwork.Artist, ", "))
	}
	link := artwork.FrontendURL
	if link == "" {
		link = "https://open.smk.dk/artwork/image/" + url.PathEscape(artwork.ObjectNumber)
	}
	urls := renditionURLs(ranked)
	attribution := attribution{Provider: p.Name(), Title: title, Artist: artist, SourceURL: link, License: smkLicense}
	return Candidate{
		Provider: p.Name(), ID: artwork.ObjectNumber, Title: title, Artist: artist, Date: date,
		ImageURL: ranked[0].url, Width: ranked[0].width, Height: ranked[0].height, License: smkLicense, Link: link,
		SourceLine: providerSMK + ":" + cmdObject + ":" + artwork.ObjectNumber,
		image: sourceImage{
			URL: urls[0], Fallbacks: urls[1:], Identity: museumObjectIdentity(p.Name(), artwork.ObjectNumber), Attribution: attribution,
		},
	}, true
}
