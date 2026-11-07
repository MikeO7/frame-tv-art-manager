package assistant

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/image/draw"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

type previewInput struct {
	Artwork string `json:"artwork" jsonschema:"filename, artwork key, or digest of a stored artwork"`
}

type previewOutput struct {
	Filename string    `json:"filename"`
	Key      string    `json:"key"`
	Width    int       `json:"preview_width"`
	Height   int       `json:"preview_height"`
	Fit      fitReport `json:"fit"`
}

type listArtworkInput struct {
	Collection string `json:"collection,omitempty" jsonschema:"only list members of this collection"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"next_cursor from a previous call"`
	Limit      int    `json:"limit,omitempty" jsonschema:"page size, 1 to 200 (default 50)"`
}

type artworkSummary struct {
	Filename      string   `json:"filename"`
	Key           string   `json:"key"`
	Digest        string   `json:"digest"`
	Width         int      `json:"width"`
	Height        int      `json:"height"`
	Collections   []string `json:"collections" jsonschema:"slugs of collections that include it; empty means everyday art"`
	SelectedToday bool     `json:"selected_today" jsonschema:"true when today's rules choose it for the TV"`
	OnTV          []string `json:"on_tv" jsonschema:"addresses of TVs whose last observed inventory held it"`
	Title         string   `json:"title,omitempty"`
	Artist        string   `json:"artist,omitempty"`
}

type listArtworkOutput struct {
	Total      int              `json:"total"`
	Artwork    []artworkSummary `json:"artwork"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func (svc *service) previewArtwork(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input previewInput,
) (*mcp.CallToolResult, previewOutput, error) {
	snapshot, err := svc.inventory(ctx)
	if err != nil {
		return nil, previewOutput{}, err
	}
	item, err := findItem(snapshot.Items, input.Artwork)
	if err != nil {
		return nil, previewOutput{}, err
	}
	rendered, err := svc.artwork.RenderPreview(ctx, item)
	if err != nil {
		return nil, previewOutput{}, err
	}
	preview, err := encodePreview(rendered)
	if err != nil {
		return nil, previewOutput{}, err
	}
	output := previewOutput{
		Filename: item.Name, Key: item.Key, Width: preview.width, Height: preview.height, Fit: svc.fitFor(item),
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: preview.jpeg, MIMEType: mediaTypeJPEG},
		&mcp.TextContent{Text: fmt.Sprintf("Preview of %s as the TV will show it (%s).", item.Name, output.Fit.Description)},
	}}
	return result, output, nil
}

type encodedPreview struct {
	jpeg          []byte
	width, height int
}

// encodePreview scales rendered to fit inside 960x540 and encodes a JPEG.
func encodePreview(rendered *image.RGBA) (encodedPreview, error) {
	bounds := rendered.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return encodedPreview{}, errors.New("the rendered preview is empty")
	}
	scale := min(float64(previewWidth)/float64(bounds.Dx()), float64(previewHeight)/float64(bounds.Dy()), 1)
	width := max(int(math.Round(float64(bounds.Dx())*scale)), 1)
	height := max(int(math.Round(float64(bounds.Dy())*scale)), 1)
	scaled := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), rendered, bounds, draw.Src, nil)
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, scaled, &jpeg.Options{Quality: previewQuality}); err != nil {
		return encodedPreview{}, fmt.Errorf("encode preview: %w", err)
	}
	return encodedPreview{jpeg: buffer.Bytes(), width: width, height: height}, nil
}

func (svc *service) listArtwork(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input listArtworkInput,
) (*mcp.CallToolResult, listArtworkOutput, error) {
	limit := input.Limit
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit < 1 || limit > maximumListLimit {
		return nil, listArtworkOutput{}, fmt.Errorf("limit must be between 1 and %d", maximumListLimit)
	}
	offset := 0
	if input.Cursor != "" {
		parsed, err := strconv.Atoi(input.Cursor)
		if err != nil || parsed < 0 {
			return nil, listArtworkOutput{}, errors.New("cursor must be a next_cursor value from list_artwork")
		}
		offset = parsed
	}
	file, err := svc.themes.Load(ctx)
	if err != nil {
		return nil, listArtworkOutput{}, err
	}
	snapshot, err := svc.inventory(ctx)
	if err != nil {
		return nil, listArtworkOutput{}, err
	}
	filter := ""
	if strings.TrimSpace(input.Collection) != "" {
		index, findErr := file.Find(input.Collection)
		if findErr != nil {
			return nil, listArtworkOutput{}, findErr
		}
		filter = file.Collections[index].Slug
	}
	summaries := svc.summarize(file, snapshot.Items, filter)
	output := listArtworkOutput{Total: len(summaries), Artwork: []artworkSummary{}}
	if offset < len(summaries) {
		end := min(offset+limit, len(summaries))
		output.Artwork = summaries[offset:end]
		if end < len(summaries) {
			output.NextCursor = strconv.Itoa(end)
		}
	}
	return nil, output, nil
}

func (svc *service) summarize(file themes.File, items []collection.Item, filter string) []artworkSummary {
	memberships := file.Memberships(items)
	selected := make(map[string]bool, len(items))
	for _, item := range themes.Select(file, items, svc.today()).Items {
		selected[item.Name] = true
	}
	placements := svc.artwork.Placements()
	summaries := make([]artworkSummary, 0, len(items))
	for index, item := range items {
		if filter != "" && !slices.Contains(memberships[index], filter) {
			continue
		}
		digest := hex.EncodeToString(item.Digest[:])
		summary := artworkSummary{
			Filename: item.Name, Key: item.Key, Digest: digest, Width: item.Width, Height: item.Height,
			Collections: memberships[index], SelectedToday: selected[item.Name],
			OnTV: append([]string{}, placements[digest]...),
		}
		summary.Title, summary.Artist = attribution(file, themes.IdentityOf(item))
		summaries = append(summaries, summary)
	}
	return summaries
}

func attribution(file themes.File, identity themes.Identity) (string, string) {
	for _, themed := range file.Collections {
		for _, member := range themed.Members {
			if member.Matches(identity) && (member.Title != "" || member.Artist != "") {
				return member.Title, member.Artist
			}
		}
	}
	return "", ""
}
