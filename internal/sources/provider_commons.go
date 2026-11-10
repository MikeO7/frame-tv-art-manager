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
	"strconv"
	"strings"
	"time"
)

const (
	providerCommons = "commons"
	// commonsMaxResponseBytes caps a decoded MediaWiki API response.
	commonsMaxResponseBytes = 10 << 20 // 10 MiB
	commonsPOTDCommand      = "potd"
	commonsFeaturedCommand  = "featured"
	commonsFeaturedCategory = "Featured_pictures_on_Wikimedia_Commons"
	// commonsThumbWidth and commonsFallbackWidth are standard Wikimedia
	// thumbnail widths; other widths are rejected or rate limited
	// (https://www.mediawiki.org/wiki/Common_thumbnail_sizes).
	commonsThumbWidth    = 3840
	commonsFallbackWidth = 1920
	commonsListLimit     = 10
	// commonsRequestInterval keeps the shared Wikimedia API at a gentle pace.
	commonsRequestInterval = 500 * time.Millisecond
)

// commonsProvider resolves Wikimedia Commons' Picture of the Day and
// Featured Pictures. Licenses vary per file (public domain, CC BY, CC BY-SA),
// so each file's license, author, and description page are recorded as its
// attribution. Requests carry the manager's descriptive User-Agent, as the
// Wikimedia User-Agent policy requires.
type commonsProvider struct {
	client  *http.Client
	gate    *rateGate
	logger  *slog.Logger
	BaseURL string
	now     func() time.Time
}

func newCommonsProvider(logger *slog.Logger) *commonsProvider {
	return &commonsProvider{
		client:  &http.Client{Timeout: 30 * time.Second},
		gate:    newPacedRateGate(commonsRequestInterval),
		logger:  logger,
		BaseURL: "https://commons.wikimedia.org/w/api.php",
		now:     time.Now,
	}
}

type commonsMetadataValue struct {
	Value string `json:"value"`
}

type commonsPage struct {
	Title     string `json:"title"`
	Index     int    `json:"index"`
	ImageInfo []struct {
		URL            string `json:"url"`
		DescriptionURL string `json:"descriptionurl"`
		ThumbURL       string `json:"thumburl"`
		ThumbWidth     int    `json:"thumbwidth"`
		ThumbHeight    int    `json:"thumbheight"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
		Mime           string `json:"mime"`
		ExtMetadata    struct {
			ObjectName       commonsMetadataValue `json:"ObjectName"`
			Artist           commonsMetadataValue `json:"Artist"`
			Credit           commonsMetadataValue `json:"Credit"`
			LicenseShortName commonsMetadataValue `json:"LicenseShortName"`
		} `json:"extmetadata"`
	} `json:"imageinfo"`
}

type commonsResponse struct {
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
	Query struct {
		Pages []commonsPage `json:"pages"`
	} `json:"query"`
}

func (p *commonsProvider) Name() string {
	return providerCommons
}

func (p *commonsProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, providerCommons+":")
}

func (p *commonsProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 {
		return nil, errors.New("invalid commons format; use commons:potd, commons:featured, or commons:search:<query>")
	}
	params := url.Values{}
	switch parts[1] {
	case commonsPOTDCommand:
		// Each day's picture is transcluded into a dated template, keyed by
		// the UTC date.
		params.Set("generator", "images")
		params.Set("titles", "Template:Potd/"+p.now().UTC().Format(time.DateOnly))
		params.Set("gimlimit", "5")
	case commonsFeaturedCommand:
		params.Set("generator", "categorymembers")
		params.Set("gcmtitle", "Category:"+commonsFeaturedCategory)
		params.Set("gcmtype", "file")
		params.Set("gcmsort", "timestamp")
		params.Set("gcmdir", "desc")
		params.Set("gcmlimit", strconv.Itoa(commonsListLimit))
	case cmdSearch:
		if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
			return nil, errors.New("commons search requires a query: commons:search:query")
		}
		params.Set("generator", "search")
		params.Set("gsrsearch", "incategory:"+commonsFeaturedCategory+" "+strings.TrimSpace(parts[2]))
		params.Set("gsrnamespace", "6")
		params.Set("gsrlimit", strconv.Itoa(commonsListLimit))
	default:
		return nil, errors.New("unknown commons type")
	}
	pages, err := p.query(ctx, params)
	if err != nil {
		return nil, err
	}
	images := make([]sourceImage, 0, len(pages))
	for _, page := range pages {
		if image, ok := p.image(page); ok {
			images = append(images, image)
		} else {
			p.logger.Info("skipping commons file without a large JPEG or PNG rendition and license", "title", page.Title)
		}
	}
	return images, nil
}

// query runs a MediaWiki imageinfo query and returns its pages in generator
// order (search relevance) where the API reports one.
func (p *commonsProvider) query(ctx context.Context, params url.Values) ([]commonsPage, error) {
	params.Set("action", "query")
	params.Set("format", "json")
	params.Set("formatversion", "2")
	params.Set("prop", "imageinfo")
	params.Set("iiprop", "url|size|mime|extmetadata")
	params.Set("iiurlwidth", strconv.Itoa(commonsThumbWidth))
	params.Set("iiextmetadatafilter", "ObjectName|Artist|Credit|LicenseShortName")
	var result commonsResponse
	if err := fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: p.BaseURL + "?" + params.Encode(),
		maxBytes: commonsMaxResponseBytes, statusLabel: "commons api", decodeLabel: "commons",
	}, &result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("commons api error %s: %s", result.Error.Code, result.Error.Info)
	}
	pages := result.Query.Pages
	slices.SortStableFunc(pages, func(a, b commonsPage) int { return a.Index - b.Index })
	return pages, nil
}

// image prefers the standard 3840-pixel thumbnail, falling back to the
// 1920-pixel one. Images no wider than 3840 are served as their original.
// A file without a license name is skipped because its credit could not be
// given.
func (p *commonsProvider) image(page commonsPage) (sourceImage, bool) {
	if len(page.ImageInfo) == 0 {
		return sourceImage{}, false
	}
	info := page.ImageInfo[0]
	license := plainText(info.ExtMetadata.LicenseShortName.Value)
	switch {
	case license == "", max(info.Width, info.Height) < minUsefulLongEdge:
		return sourceImage{}, false
	case info.Mime != sourceMediaTypeJPEG && info.Mime != sourceMediaTypePNG && info.Mime != "image/tiff":
		return sourceImage{}, false
	}
	primary := info.ThumbURL
	if primary == "" {
		primary = info.URL
	}
	renditions := []rendition{{url: primary, width: info.ThumbWidth, height: info.ThumbHeight}}
	thumbSegment, fallbackSegment := "/"+strconv.Itoa(commonsThumbWidth)+"px-", "/"+strconv.Itoa(commonsFallbackWidth)+"px-"
	if fallback := strings.Replace(primary, thumbSegment, fallbackSegment, 1); fallback != primary {
		width, height := scaledSize(info.Width, info.Height, axisWidth, commonsFallbackWidth)
		renditions = append(renditions, rendition{url: fallback, width: width, height: height})
	}
	var urls []string
	for _, candidate := range renditions {
		if isSupportedImageURL(candidate.url) && candidate.pixels() <= maxDownloadedPixels {
			urls = append(urls, candidate.url)
		}
	}
	if len(urls) == 0 {
		return sourceImage{}, false
	}
	// The file name is stable, unlike its descriptive metadata, so it labels
	// the identity.
	fileName := strings.TrimPrefix(page.Title, "File:")
	title := plainText(info.ExtMetadata.ObjectName.Value)
	if title == "" {
		title = strings.TrimSuffix(fileName, path.Ext(fileName))
	}
	artist := plainText(info.ExtMetadata.Artist.Value)
	if artist == "" {
		artist = plainText(info.ExtMetadata.Credit.Value)
	}
	return sourceImage{
		URL: urls[0], Fallbacks: urls[1:], Identity: sourceIdentity(p.Name(), page.Title, fileName),
		Attribution: attribution{Provider: p.Name(), Title: title, Artist: artist, SourceURL: info.DescriptionURL, License: license},
	}, true
}
