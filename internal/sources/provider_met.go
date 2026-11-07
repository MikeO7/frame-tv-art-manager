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
	"sync"
	"time"
)

const (
	providerMet = "met"
	// metMaxResponseBytes caps a decoded Met API response to bound memory.
	metMaxResponseBytes = 4 << 20 // 4 MiB
	// metRequestInterval spaces Met requests to at most 8 per second, a tenth
	// of the 80 per second the API allows: a search costs one request per
	// object it inspects.
	metRequestInterval = 125 * time.Millisecond
	// metScanFactor bounds how many search hits are inspected per wanted
	// work, since many hits are not public domain or have no image.
	metScanFactor = 3
	// metMaxScan caps the objects one search inspects, about 8 seconds of
	// requests at the paced rate.
	metMaxScan = 64
	// metObjectCacheTTL and metObjectCacheSize bound the in-memory object
	// cache shared by search lines, object lines, and ArtSearcher queries.
	metObjectCacheTTL  = 24 * time.Hour
	metObjectCacheSize = 2048
)

// metProvider resolves works from The Metropolitan Museum of Art Collection
// API, which needs no key. Searches return object IDs only, so each hit costs
// a further object request; objects are cached in memory and requests paced.
type metProvider struct {
	client  *http.Client
	gate    *rateGate
	logger  *slog.Logger
	BaseURL string

	mu      sync.Mutex
	objects map[int]cachedMetObject
	now     func() time.Time
}

type cachedMetObject struct {
	object  metObject
	expires time.Time
}

func newMetProvider(logger *slog.Logger) *metProvider {
	return &metProvider{
		client:  &http.Client{Timeout: 30 * time.Second},
		gate:    newPacedRateGate(metRequestInterval),
		logger:  logger,
		BaseURL: "https://collectionapi.metmuseum.org/public/collection",
		objects: make(map[int]cachedMetObject),
		now:     time.Now,
	}
}

// metObject is the subset of a Met object record the provider uses.
type metObject struct {
	ObjectID          int    `json:"objectID"`
	IsPublicDomain    bool   `json:"isPublicDomain"`
	PrimaryImage      string `json:"primaryImage"`
	Title             string `json:"title"`
	ArtistDisplayName string `json:"artistDisplayName"`
	ObjectDate        string `json:"objectDate"`
	ObjectURL         string `json:"objectURL"`
}

func (p *metProvider) Name() string {
	return providerMet
}

func (p *metProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, providerMet+":")
}

func (p *metProvider) fetchJSON(ctx context.Context, apiURL string, out any) error {
	return fetchProviderJSON(ctx, providerJSONRequest{
		client: p.client, gate: p.gate, provider: p.Name(), url: apiURL,
		maxBytes: metMaxResponseBytes, statusLabel: "met api", decodeLabel: "met",
	}, out)
}

func (p *metProvider) Resolve(ctx context.Context, line string) ([]SourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 || parts[2] == "" {
		return nil, errors.New("invalid met format; use met:search:<query> or met:object:<id>")
	}
	switch parts[1] {
	case cmdSearch:
		query, filters, err := searchArgument(parts[2],
			"departmentId", "isHighlight", "isOnView", "artistOrCulture", "title", "tags", "medium", "geoLocation", "dateBegin", "dateEnd")
		if err != nil {
			return nil, fmt.Errorf("met search: %w", err)
		}
		candidates, err := p.search(ctx, query, filters, museumSearchLimit)
		if err != nil {
			return nil, err
		}
		return candidateImages(candidates), nil
	case cmdObject:
		id, err := strconv.Atoi(parts[2])
		if err != nil || id <= 0 {
			return nil, errors.New("met object requires a numeric object ID")
		}
		object, err := p.object(ctx, id)
		if err != nil {
			return nil, err
		}
		candidate, ok := p.candidate(object)
		if !ok {
			return nil, errors.New("met object is not public domain or has no open-access image")
		}
		return []SourceImage{candidate.image}, nil
	default:
		return nil, errors.New("unknown met type")
	}
}

func (p *metProvider) searchCandidates(ctx context.Context, query string, limit int) ([]Candidate, error) {
	return p.search(ctx, query, url.Values{}, limit)
}

// search inspects up to metScanFactor hits per wanted work, at most
// metMaxScan. A hit whose
// object cannot be read is skipped; the search fails only when every
// inspected object failed or the provider asked for no more requests.
func (p *metProvider) search(ctx context.Context, query string, filters url.Values, limit int) ([]Candidate, error) {
	limit = searchLimit(limit)
	params := url.Values{"hasImages": {"true"}, paramLimit: {strconv.Itoa(min(limit*metScanFactor, metMaxScan))}}
	for name, values := range filters {
		params[name] = values
	}
	if query != "" {
		params.Set("q", query)
	}
	// The v1 search endpoint was retired on 2026-10-01; v1.1 pages its
	// results (https://metmuseum.github.io/#search-v11).
	var result struct {
		ObjectIDs []int `json:"objectIDs"`
	}
	if err := p.fetchJSON(ctx, p.BaseURL+"/v1.1/search?"+params.Encode(), &result); err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, limit)
	var failures []error
	for _, id := range result.ObjectIDs {
		if len(candidates) == limit {
			break
		}
		object, err := p.object(ctx, id)
		if err != nil {
			var limited *rateLimitError
			if ctx.Err() != nil || errors.As(err, &limited) {
				return nil, err
			}
			p.logger.Debug("skipping met object that could not be read", "id", id, "error", err)
			failures = append(failures, fmt.Errorf("met object %d: %w", id, err))
			continue
		}
		if candidate, ok := p.candidate(object); ok {
			candidates = append(candidates, candidate)
		}
	}
	if len(failures) > 0 && len(failures) == len(result.ObjectIDs) {
		return nil, errors.Join(failures...)
	}
	return candidates, nil
}

// object returns a Met object record, from the cache when it is fresh.
func (p *metProvider) object(ctx context.Context, id int) (metObject, error) {
	now := p.now()
	p.mu.Lock()
	cached, ok := p.objects[id]
	p.mu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.object, nil
	}
	var object metObject
	if err := p.fetchJSON(ctx, p.BaseURL+"/v1/objects/"+strconv.Itoa(id), &object); err != nil {
		return metObject{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.objects) >= metObjectCacheSize {
		for key, entry := range p.objects {
			if !now.Before(entry.expires) {
				delete(p.objects, key)
			}
		}
		if len(p.objects) >= metObjectCacheSize {
			clear(p.objects)
		}
	}
	p.objects[id] = cachedMetObject{object: object, expires: now.Add(metObjectCacheTTL)}
	return object, nil
}

// candidate accepts public-domain objects with a full-size primary image.
// The Met publishes no pixel dimensions, and its only smaller rendition is
// far below panel size, so the full-size image has no fallback.
func (p *metProvider) candidate(object metObject) (Candidate, bool) {
	if !object.IsPublicDomain || object.ObjectID <= 0 || !isSupportedImageURL(object.PrimaryImage) {
		return Candidate{}, false
	}
	id := strconv.Itoa(object.ObjectID)
	link := object.ObjectURL
	if link == "" {
		link = "https://www.metmuseum.org/art/collection/search/" + id
	}
	attribution := Attribution{
		Provider: p.Name(), Title: plainText(object.Title), Artist: plainText(object.ArtistDisplayName),
		SourceURL: link, License: publicDomainCC0,
	}
	return Candidate{
		Provider: p.Name(), ID: id, Title: attribution.Title, Artist: attribution.Artist, Date: plainText(object.ObjectDate),
		ImageURL: object.PrimaryImage, License: publicDomainCC0, Link: link, SourceLine: providerMet + ":" + cmdObject + ":" + id,
		image: SourceImage{URL: object.PrimaryImage, Identity: museumObjectIdentity(p.Name(), id), Attribution: attribution},
	}, true
}
