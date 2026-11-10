package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

const (
	defaultSearchLimit    = 12
	maximumSearchLimit    = 50
	searchProviderTimeout = 20 * time.Second
)

// artSearch finds public-domain works in museum collections.
type artSearch interface {
	Search(context.Context, sources.SearchQuery) (sources.SearchResult, error)
	Providers() []string
}

type searchArtInput struct {
	Query     string   `json:"query" jsonschema:"free-text search, for example christmas or dutch winter landscape"`
	Limit     int      `json:"limit,omitempty" jsonschema:"most candidates to return, 1 to 50 (default 12)"`
	Providers []string `json:"providers,omitempty" jsonschema:"restrict the search to these museum providers; empty searches all"`
}

type searchCandidate struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Date     string `json:"date,omitempty"`
	ImageURL string `json:"image_url" jsonschema:"pass this to add_artwork as image_url"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	License  string `json:"license"`
	Link     string `json:"link,omitempty" jsonschema:"the work's page on the museum website"`
	// Fit predicts how the TV will show the image when its size is known.
	Fit string `json:"fit,omitempty" jsonschema:"exact, crop, mat, blur, or collage; empty when the size is unknown"`
	// SourceLine adds the work through ARTWORK_SOURCES_FILE instead.
	SourceLine string `json:"source_line"`
}

type searchArtOutput struct {
	Query      string                    `json:"query"`
	Candidates []searchCandidate         `json:"candidates"`
	Failures   []sources.ProviderFailure `json:"failures,omitempty" jsonschema:"museums that could not answer this time"`
	Providers  []string                  `json:"providers" jsonschema:"provider names accepted by the providers argument"`
}

func (svc *service) registerSearchTools(server *mcp.Server) {
	if svc.searcher == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:  "search_public_domain_art",
		Title: "Search public-domain museum art",
		Description: "Searches public-domain collections (" + strings.Join(svc.searcher.Providers(), ", ") +
			") for real artwork instead of generating images. Each candidate has an image_url, title, artist, " +
			"license, and museum link; to keep one, call add_artwork with image_url set to the candidate's " +
			"image_url and pass its title, artist, license, and link so the credit is recorded. Museums that fail " +
			"are listed in failures while the others still answer. Nothing is stored and the TV is not changed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(true)},
	}, svc.searchArt)
}

func (svc *service) searchArt(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input searchArtInput,
) (*mcp.CallToolResult, searchArtOutput, error) {
	text := strings.TrimSpace(input.Query)
	if text == "" {
		return nil, searchArtOutput{}, errors.New("query is required, for example \"christmas\"")
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if limit < 1 || limit > maximumSearchLimit {
		return nil, searchArtOutput{}, fmt.Errorf("limit must be between 1 and %d", maximumSearchLimit)
	}
	result, err := svc.searcher.Search(ctx, sources.SearchQuery{
		Text: text, Limit: limit, Providers: input.Providers, ProviderTimeout: searchProviderTimeout,
	})
	if err != nil {
		return nil, searchArtOutput{}, fmt.Errorf("search public-domain art: %w", err)
	}
	output := searchArtOutput{
		Query: text, Candidates: make([]searchCandidate, 0, len(result.Candidates)),
		Failures: result.Failures, Providers: svc.searcher.Providers(),
	}
	for _, candidate := range result.Candidates {
		output.Candidates = append(output.Candidates, svc.describeCandidate(candidate))
	}
	svc.logger.Info("public-domain art search", "candidates", len(output.Candidates), "failed_providers", len(output.Failures))
	return nil, output, nil
}

func (svc *service) describeCandidate(candidate sources.Candidate) searchCandidate {
	described := searchCandidate{
		Provider: candidate.Provider, ID: candidate.ID, Title: candidate.Title, Artist: candidate.Artist,
		Date: candidate.Date, ImageURL: candidate.ImageURL, Width: candidate.Width, Height: candidate.Height,
		License: candidate.License, Link: candidate.Link, SourceLine: candidate.SourceLine,
	}
	if candidate.Width > 0 && candidate.Height > 0 {
		fit := optimize.DescribeFit(candidate.Width, candidate.Height, svc.cfg.OptimizeOptions())
		if fit.Mode != optimize.FitUnchanged {
			described.Fit = string(fit.Mode)
		}
	}
	return described
}
