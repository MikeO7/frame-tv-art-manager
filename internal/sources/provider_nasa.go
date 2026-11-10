package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

const (
	// nasaMaxResponseBytes caps a decoded NASA API response to bound memory use.
	nasaMaxResponseBytes = 10 << 20 // 10 MiB
	// nasaMaxSearchItems limits image-library search results to avoid huge downloads.
	nasaMaxSearchItems = 10
	nasaLibraryLicense = "NASA Images and Media Usage Guidelines"
	nasaPublicDomain   = "Public domain (NASA)"
	apodCommand        = "apod"
)

// nasaProvider handles communication with NASA APIs and resolves artwork sources.
type nasaProvider struct {
	apiKey    string
	client    *http.Client
	gate      *rateGate
	logger    *slog.Logger
	BaseURL   string
	SearchURL string
}

// newNasaProvider creates a new NASA provider. config.LoadEnviron defaults an
// unset NASA_API_KEY to DEMO_KEY.
func newNasaProvider(apiKey string, logger *slog.Logger) *nasaProvider {
	return &nasaProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		gate:      newRateGate(),
		logger:    logger,
		BaseURL:   "https://api.nasa.gov",
		SearchURL: "https://images-api.nasa.gov",
	}
}

// apodResponse represents the response from NASA's APOD API.
type apodResponse struct {
	Date      string `json:"date"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	HDURL     string `json:"hdurl"`
	Type      string `json:"media_type"`
	Copyright string `json:"copyright"`
}

type nasaLibraryItem struct {
	Href string `json:"href"` // asset manifest URL
	Data []struct {
		NASAID           string `json:"nasa_id"`
		Title            string `json:"title"`
		Photographer     string `json:"photographer"`
		SecondaryCreator string `json:"secondary_creator"`
		Center           string `json:"center"`
	} `json:"data"`
}

// fetchJSON issues a GET to apiURL and decodes the size-bounded JSON response
// body into out, centralizing the NASA request boilerplate.
func (p *nasaProvider) fetchJSON(ctx context.Context, apiURL, errLabel string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL, maxBytes: nasaMaxResponseBytes,
		statusLabel: errLabel, decodeLabel: errLabel,
	}, out)
}

func (p *nasaProvider) Name() string {
	return providerNASA
}

func (p *nasaProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, "nasa:")
}

func (p *nasaProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 {
		return nil, errors.New("invalid nasa format")
	}
	switch parts[1] {
	case apodCommand:
		return p.resolveAPOD(ctx)
	case cmdSearch:
		if len(parts) < 3 || parts[2] == "" {
			return nil, errors.New("nasa search requires a query: nasa:search:query")
		}
		return p.searchLibrary(ctx, parts[2])
	default:
		return nil, errors.New("unknown nasa type")
	}
}

// resolveAPOD resolves today's Astronomy Picture of the Day. Days whose entry
// is a video or another non-image medium resolve to no images.
func (p *nasaProvider) resolveAPOD(ctx context.Context) ([]sourceImage, error) {
	apiURL := fmt.Sprintf("%s/planetary/apod?api_key=%s", p.BaseURL, url.QueryEscape(p.apiKey))
	var apod apodResponse
	if err := p.fetchJSON(ctx, apiURL, "nasa apod api", &apod); err != nil {
		return nil, err
	}
	if apod.Type != mediaTypeImage {
		p.logger.Info("skipping astronomy picture of the day that is not an image", "media_type", apod.Type)
		return []sourceImage{}, nil
	}
	var candidates []string
	for _, candidate := range []string{apod.HDURL, apod.URL} {
		if isSupportedImageURL(candidate) && !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		p.logger.Info("skipping astronomy picture of the day without a JPEG or PNG image")
		return []sourceImage{}, nil
	}
	artist := strings.Join(strings.Fields(apod.Copyright), " ")
	license := nasaPublicDomain
	if artist != "" {
		license = "Copyright " + artist
	}
	return []sourceImage{{
		URL: candidates[0], Fallbacks: candidates[1:], Identity: sourceURLIdentity(p.Name(), candidates[0]),
		Attribution: attribution{
			Provider: p.Name(), Title: apod.Title, Artist: artist, SourceURL: apodPageURL(apod.Date), License: license,
		},
	}}, nil
}

// apodPageURL returns the archive page for an APOD date, or "" when the date
// is malformed.
func apodPageURL(date string) string {
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return ""
	}
	return "https://apod.nasa.gov/apod/ap" + parsed.Format("060102") + ".html"
}

// searchLibrary resolves the first image-library search results. An item
// whose asset manifest cannot be read is skipped; the search fails only when
// every manifest failed.
func (p *nasaProvider) searchLibrary(ctx context.Context, query string) ([]sourceImage, error) {
	searchURL := fmt.Sprintf("%s/search?q=%s&media_type=image", p.SearchURL, url.QueryEscape(query))
	var result struct {
		Collection struct {
			Items []nasaLibraryItem `json:"items"`
		} `json:"collection"`
	}
	if err := p.fetchJSON(ctx, searchURL, "nasa image library", &result); err != nil {
		return nil, err
	}

	items := result.Collection.Items[:min(len(result.Collection.Items), nasaMaxSearchItems)]
	images := make([]sourceImage, 0, len(items))
	var failures []error
	for index, item := range items {
		image, ok, err := p.libraryImage(ctx, item)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("resolve nasa image library: %w", ctx.Err())
			}
			p.logger.Warn("skipping nasa image whose asset manifest failed", "href", truncateURL(item.Href), "error", err)
			failures = append(failures, fmt.Errorf("resolve NASA asset %d: %w", index+1, err))
			continue
		}
		if ok {
			images = append(images, image)
		}
	}
	if len(items) > 0 && len(failures) == len(items) {
		return nil, errors.Join(failures...)
	}
	return images, nil
}

// libraryImage selects the original rendition with the large rendition as its
// fallback when the original exceeds the download or pixel caps.
func (p *nasaProvider) libraryImage(ctx context.Context, item nasaLibraryItem) (sourceImage, bool, error) {
	var manifest []string
	if err := p.fetchJSON(ctx, item.Href, "nasa asset manifest api", &manifest); err != nil {
		return sourceImage{}, false, err
	}
	var original, large string
	for _, asset := range manifest {
		switch {
		case strings.HasSuffix(asset, "~orig.jpg"):
			original = asset
		case strings.HasSuffix(asset, "~large.jpg"):
			large = asset
		}
	}
	candidates := make([]string, 0, 2)
	for _, candidate := range []string{original, large} {
		if candidate != "" {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return sourceImage{}, false, nil
	}
	attribution := attribution{Provider: p.Name(), License: nasaLibraryLicense}
	if len(item.Data) > 0 {
		data := item.Data[0]
		attribution.Title = data.Title
		attribution.Artist = firstNonEmpty(data.Photographer, data.SecondaryCreator, data.Center)
		if data.NASAID != "" {
			attribution.SourceURL = "https://images.nasa.gov/details/" + url.PathEscape(data.NASAID)
		}
	}
	return sourceImage{
		URL: candidates[0], Fallbacks: candidates[1:], Identity: sourceURLIdentity(p.Name(), candidates[0]),
		Attribution: attribution,
	}, true, nil
}

// isSupportedImageURL reports whether rawURL is an HTTP(S) URL whose path
// names a JPEG or PNG file.
func isSupportedImageURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(path.Ext(parsed.Path)) {
	case extJPG, ".jpeg", extPNG:
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
