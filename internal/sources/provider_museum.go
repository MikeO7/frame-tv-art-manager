package sources

import (
	"html"
	neturl "net/url"
	"regexp"
	"slices"
	"strings"
)

const (
	// cmdObject resolves one museum object by its collection identifier.
	cmdObject = "object"
	// publicDomainCC0 is the license of every open-access museum image the
	// museum providers accept.
	publicDomainCC0 = "CC0 1.0 (public domain)"
	// minUsefulLongEdge is the smallest long edge, in pixels, worth showing on
	// the 3840×2160 panel; smaller images are skipped when their size is known.
	minUsefulLongEdge = 1920
	// museumSearchLimit is the number of works one museum search line resolves.
	museumSearchLimit = 10
	// maxMuseumSearchLimit bounds the works requested from one provider.
	maxMuseumSearchLimit = 50
	// paramLimit is the result-count query parameter several museum APIs share.
	paramLimit = "limit"
)

// reHTMLTag matches markup in provider metadata such as Wikimedia credits.
var reHTMLTag = regexp.MustCompile(`<[^>]*>`)

// plainText strips markup and entities from provider metadata and collapses
// whitespace so it reads cleanly in /status.
func plainText(value string) string {
	value = html.UnescapeString(reHTMLTag.ReplaceAllString(value, " "))
	return strings.Join(strings.Fields(value), " ")
}

// searchArgument splits a "query?name=value&..." search argument into the
// free-text query and provider filters. Filters outside allowed are rejected
// so that a typo cannot silently widen a search, and filters the provider
// enforces itself (such as public-domain only) can never be overridden.
func searchArgument(argument string, allowed ...string) (string, neturl.Values, error) {
	query, rawFilters, hasFilters := strings.Cut(argument, "?")
	query = strings.TrimSpace(query)
	filters := neturl.Values{}
	if hasFilters {
		parsed, err := neturl.ParseQuery(rawFilters)
		if err != nil {
			return "", nil, configError("parse search filters: %v", err)
		}
		for name := range parsed {
			if !slices.Contains(allowed, name) {
				if len(allowed) == 0 {
					return "", nil, configError("search filter %q is not supported; this provider takes no filters", name)
				}
				return "", nil, configError("search filter %q is not supported; use one of %s", name, strings.Join(allowed, ", "))
			}
		}
		filters = parsed
	}
	if query == "" && len(filters) == 0 {
		return "", nil, configError("search requires a query or at least one filter")
	}
	return query, filters, nil
}

// rendition is one downloadable size of an artwork image; zero dimensions
// mean the provider did not publish them.
type rendition struct {
	url    string
	width  int
	height int
}

// covers reports whether the rendition fills the panel without enlargement.
func (r rendition) covers() bool {
	return r.width >= targetArtworkWidth && r.height >= targetArtworkHeight
}

func (r rendition) pixels() int64 {
	return int64(r.width) * int64(r.height)
}

// usable reports whether the rendition is a JPEG or PNG within the download
// pixel cap and, when its size is known, large enough for the panel.
func (r rendition) usable() bool {
	if !isSupportedImageURL(r.url) {
		return false
	}
	if r.width <= 0 || r.height <= 0 {
		return true
	}
	return r.pixels() <= maxDownloadedPixels && max(r.width, r.height) >= minUsefulLongEdge
}

// rankRenditions keeps the usable renditions, smallest panel-covering one
// first and then the rest from largest to smallest, so the download is as
// sharp as the panel needs without being needlessly enormous.
func rankRenditions(renditions []rendition) []rendition {
	var covering, smaller []rendition
	for _, candidate := range renditions {
		switch {
		case !candidate.usable():
			continue
		case candidate.covers():
			covering = append(covering, candidate)
		default:
			smaller = append(smaller, candidate)
		}
	}
	slices.SortStableFunc(covering, func(a, b rendition) int { return compareInt64(a.pixels(), b.pixels()) })
	slices.SortStableFunc(smaller, func(a, b rendition) int { return compareInt64(b.pixels(), a.pixels()) })
	return append(covering, smaller...)
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// renditionURLs returns the ranked renditions' URLs, preferred first.
func renditionURLs(renditions []rendition) []string {
	urls := make([]string, 0, len(renditions))
	for _, candidate := range renditions {
		if !slices.Contains(urls, candidate.url) {
			urls = append(urls, candidate.url)
		}
	}
	return urls
}

// scaledSize returns the size of a width×height image scaled so its axis
// edge is size pixels, or zeros when the source size is unknown.
func scaledSize(width, height int, axis string, size int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	if axis == axisHeight {
		return int(int64(size) * int64(width) / int64(height)), size
	}
	return size, int(int64(size) * int64(height) / int64(width))
}

// museumObjectIdentity keys a museum work by its collection identifier, so a
// re-photographed image or an edited title never duplicates the artwork.
func museumObjectIdentity(provider, objectID string) string {
	return sourceIdentity(provider, provider+":"+cmdObject+":"+objectID, objectID)
}

// candidateImages returns the downloadable sources behind search candidates.
func candidateImages(candidates []Candidate) []sourceImage {
	images := make([]sourceImage, 0, len(candidates))
	for _, candidate := range candidates {
		images = append(images, candidate.image)
	}
	return images
}

// searchLimit clamps a requested result count to a provider's bounds.
func searchLimit(limit int) int {
	if limit <= 0 {
		return museumSearchLimit
	}
	return min(limit, maxMuseumSearchLimit)
}
