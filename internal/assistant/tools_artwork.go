package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

const (
	previewWidth       = 960
	previewHeight      = 540
	previewQuality     = 85
	smallUpscaleLimit  = 1.5
	noticeableCrop     = 5.0
	defaultListLimit   = 50
	maximumListLimit   = 200
	uploadOriginPrefix = "upload:"
)

type addArtworkInput struct {
	Collection  string `json:"collection" jsonschema:"themed collection name, for example Christmas; created (off, exclusive) when missing"`
	ImageURL    string `json:"image_url,omitempty" jsonschema:"https link to a JPEG or PNG image; use this or image_base64"`
	ImageBase64 string `json:"image_base64,omitempty" jsonschema:"base64 JPEG or PNG bytes, optionally as a data: URL; use this or image_url"`
	Title       string `json:"title,omitempty" jsonschema:"title of the artwork, also used in the filename"`
	Artist      string `json:"artist,omitempty" jsonschema:"artist or model credit"`
	Description string `json:"description,omitempty" jsonschema:"short description or the prompt used"`
	License     string `json:"license,omitempty" jsonschema:"license of a found work, for example from search_public_domain_art"`
	Link        string `json:"link,omitempty" jsonschema:"http or https page about the work, for example its museum page"`
}

type fitReport struct {
	Mode        string  `json:"mode" jsonschema:"exact, crop, mat, blur, collage, or unchanged"`
	Scale       float64 `json:"scale" jsonschema:"enlargement factor; above 1 means the image is upscaled"`
	CropPercent float64 `json:"crop_percent,omitempty" jsonschema:"share of the picture cut away by a crop"`
	Description string  `json:"description"`
}

type addArtworkOutput struct {
	Collection          string    `json:"collection" jsonschema:"collection slug"`
	CollectionCreated   bool      `json:"collection_created"`
	Filename            string    `json:"filename" jsonschema:"stored filename in the artwork folder"`
	Key                 string    `json:"key" jsonschema:"stable artwork key; use it with preview_artwork and remove_from_collection"`
	Digest              string    `json:"digest" jsonschema:"SHA-256 of the stored bytes"`
	Duplicate           bool      `json:"duplicate" jsonschema:"true when identical artwork was already stored"`
	AlreadyInCollection bool      `json:"already_in_collection"`
	Width               int       `json:"width"`
	Height              int       `json:"height"`
	Bytes               int64     `json:"bytes"`
	Fit                 fitReport `json:"fit"`
	CollectionActive    bool      `json:"collection_active_today"`
	Warnings            []string  `json:"warnings"`
}

func (svc *service) registerArtworkTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:  "add_artwork",
		Title: "Add an image to a themed collection",
		Description: "Stores one JPEG or PNG image in the artwork folder and adds it to a themed collection, creating " +
			"the collection when it does not exist (new collections are off and exclusive until update_collection " +
			"schedules them). Provide exactly one of image_url (https) or image_base64. The image is validated and " +
			"deduplicated like a web upload. Returns the stored filename and key and how the image will be fitted; " +
			"heed warnings about small or heavily cropped images and regenerate at 16:9.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(true)},
	}, svc.addArtwork)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "preview_artwork",
		Title: "Preview artwork as the TV will show it",
		Description: "Renders a stored artwork through the same crop, mat, color, and sharpening pipeline used for the " +
			"TV and returns a 960x540 JPEG preview. Nothing is saved and the TV is not changed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	}, svc.previewArtwork)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "list_artwork",
		Title: "List stored artwork",
		Description: "Lists the artwork in the folder a page at a time, with each picture's filename, key, size, " +
			"collections, whether today's rules select it, and which TVs last held it. Filter by collection.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	}, svc.listArtwork)
}

func (svc *service) addArtwork(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input addArtworkInput,
) (*mcp.CallToolResult, addArtworkOutput, error) {
	if err := svc.rejectDryRun(); err != nil {
		return nil, addArtworkOutput{}, err
	}
	slug, err := themes.Slug(input.Collection)
	if err != nil {
		return nil, addArtworkOutput{}, err
	}
	link := strings.TrimSpace(input.Link)
	if err := themes.ValidateLink(link); err != nil {
		return nil, addArtworkOutput{}, err
	}
	data, err := svc.imageBytes(ctx, input)
	if err != nil {
		return nil, addArtworkOutput{}, err
	}
	digest := sha256.Sum256(data)
	member := themes.Member{
		Origin: uploadOriginPrefix + hex.EncodeToString(digest[:]), Digest: hex.EncodeToString(digest[:]),
		Title: strings.TrimSpace(input.Title), Artist: strings.TrimSpace(input.Artist),
		Description: strings.TrimSpace(input.Description), License: strings.TrimSpace(input.License), Link: link,
		AddedAt: svc.now().UTC(),
	}
	// Membership is recorded before the bytes are published so a crash
	// between the two can never leave themed art in the everyday rotation.
	reservation, err := svc.reserveMember(ctx, input.Collection, member)
	if err != nil {
		return nil, addArtworkOutput{}, err
	}
	snapshot, err := svc.artwork.Import(ctx, collection.ImportRequest{
		Reader: bytes.NewReader(data), Hint: importHint(input.Title, slug), MaxBytes: svc.maxImageBytes(),
	})
	if err != nil {
		return nil, addArtworkOutput{}, errors.Join(importFailure(err), svc.releaseMember(ctx, slug, member, reservation))
	}
	item, kind, err := committedItem(snapshot)
	if err != nil {
		// The image is stored and its membership recorded, so this is a
		// success whose details cannot be described.
		svc.logger.Error("describe stored artwork failed", "collection", slug, "error", err)
		return nil, svc.describeUnverified(snapshot, slug, member, reservation), nil
	}
	output := svc.describeAdded(item, kind, slug, reservation)
	member.Key, member.Name = item.Key, item.Name
	file, err := svc.themes.Update(ctx, func(file *themes.File) error {
		index, findErr := file.Find(slug)
		if findErr != nil {
			return findErr
		}
		_, addErr := file.Collections[index].AddMember(member)
		return addErr
	})
	if err != nil {
		output.Warnings = append(output.Warnings,
			"the artwork key could not be recorded; membership still follows the image's upload identity")
		svc.logger.Warn("record themed member key failed", "collection", slug, "error", err)
	} else if index, findErr := file.Find(slug); findErr == nil {
		output.CollectionActive = file.Collections[index].Activation.ActiveAt(svc.today())
		if !output.CollectionActive {
			output.Warnings = append(output.Warnings, fmt.Sprintf(
				"collection %q is not active today (%s), so this picture is hidden on the TV until update_collection schedules it",
				slug, file.Collections[index].Activation.Describe()))
		}
	}
	svc.logger.Info("artwork added to themed collection",
		"collection", slug, "file", item.Name, "duplicate", output.Duplicate, "width", item.Width, "height", item.Height)
	return nil, output, nil
}

// memberReservation records what reserveMember created so a failed import
// can undo exactly that.
type memberReservation struct {
	createdCollection bool
	addedMember       bool
}

func (svc *service) reserveMember(ctx context.Context, name string, member themes.Member) (memberReservation, error) {
	var reservation memberReservation
	_, err := svc.themes.Update(ctx, func(file *themes.File) error {
		reservation = memberReservation{}
		index, findErr := file.Find(name)
		if errors.Is(findErr, themes.ErrNotFound) {
			created, newErr := themes.NewCollection(name, "", true, themes.Activation{Mode: themes.ModeOff})
			if newErr != nil {
				return newErr
			}
			if addErr := file.Add(created); addErr != nil {
				return addErr
			}
			reservation.createdCollection = true
			index = len(file.Collections) - 1
		} else if findErr != nil {
			return findErr
		}
		added, addErr := file.Collections[index].AddMember(member)
		reservation.addedMember = added
		return addErr
	})
	if err != nil {
		return memberReservation{}, fmt.Errorf("record collection membership: %w", err)
	}
	return reservation, nil
}

func (svc *service) releaseMember(ctx context.Context, slug string, member themes.Member, reservation memberReservation) error {
	if !reservation.addedMember && !reservation.createdCollection {
		return nil
	}
	_, err := svc.themes.Update(context.WithoutCancel(ctx), func(file *themes.File) error {
		index, findErr := file.Find(slug)
		if findErr != nil {
			return nil //nolint:nilerr // the reservation is already gone
		}
		if reservation.addedMember {
			file.Collections[index].RemoveMember(themes.Identity{Origin: member.Origin, Digest: member.Digest})
		}
		if reservation.createdCollection && len(file.Collections[index].Members) == 0 {
			file.Collections = slices.Delete(file.Collections, index, index+1)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("undo collection membership after the failed import: %w", err)
	}
	return nil
}

func importFailure(err error) error {
	if errors.Is(err, collection.ErrInvalidImport) {
		return fmt.Errorf("the image was rejected (not a valid, safe JPEG or PNG within the size and pixel limits): %w", err)
	}
	return fmt.Errorf("store the image: %w", err)
}

func committedItem(snapshot collection.Snapshot) (collection.Item, collection.ChangeKind, error) {
	if len(snapshot.Changes) != 1 || snapshot.Changes[0].Name == "" {
		return collection.Item{}, "", errors.New("the stored image could not be verified")
	}
	change := snapshot.Changes[0]
	index := slices.IndexFunc(snapshot.Items, func(item collection.Item) bool { return item.Name == change.Name })
	if index < 0 {
		return collection.Item{}, "", fmt.Errorf("the stored image %q is missing from the verified folder", change.Name)
	}
	return snapshot.Items[index], change.Kind, nil
}

// describeUnverified reports an import that was committed but whose stored
// item could not be found in the returned snapshot.
func (svc *service) describeUnverified(
	snapshot collection.Snapshot, slug string, member themes.Member, reservation memberReservation,
) addArtworkOutput {
	output := addArtworkOutput{
		Collection: slug, CollectionCreated: reservation.createdCollection, Digest: member.Digest,
		AlreadyInCollection: !reservation.addedMember,
		Warnings: []string{"the image was saved and added to the collection, but its details are unavailable; " +
			"call list_artwork to see its filename, key, and fit"},
	}
	if len(snapshot.Changes) == 1 {
		output.Filename = snapshot.Changes[0].Name
		output.Duplicate = snapshot.Changes[0].Kind == collection.ChangeDuplicate
	}
	return output
}

func (svc *service) describeAdded(item collection.Item, kind collection.ChangeKind, slug string, reservation memberReservation) addArtworkOutput {
	fit := svc.fitFor(item)
	output := addArtworkOutput{
		Collection: slug, CollectionCreated: reservation.createdCollection,
		Filename: item.Name, Key: item.Key, Digest: hex.EncodeToString(item.Digest[:]),
		Duplicate: kind == collection.ChangeDuplicate, AlreadyInCollection: !reservation.addedMember,
		Width: item.Width, Height: item.Height, Bytes: item.Size, Fit: fit, Warnings: []string{},
	}
	if fit.Scale > smallUpscaleLimit {
		output.Warnings = append(output.Warnings, fmt.Sprintf(
			"the image is small (%dx%d) and will be enlarged %.2fx; regenerate it at %dx%d or the largest 16:9 size available",
			item.Width, item.Height, fit.Scale, svc.cfg.OptimizeMaxWidth, svc.cfg.OptimizeMaxHeight))
	}
	if fit.Mode == string(optimize.FitCrop) && fit.CropPercent > noticeableCrop {
		output.Warnings = append(output.Warnings, fmt.Sprintf(
			"about %.0f%% of the picture is cropped away to fill the screen; generate at 16:9 to keep the whole composition",
			fit.CropPercent))
	}
	if output.Duplicate {
		output.Warnings = append(output.Warnings,
			"identical artwork was already stored; it now also belongs to this collection")
	}
	return output
}

func (svc *service) fitFor(item collection.Item) fitReport {
	fit := optimize.DescribeFit(item.Width, item.Height, svc.cfg.OptimizeOptions())
	report := fitReport{Mode: string(fit.Mode), Scale: round(fit.Scale, 3), CropPercent: round(fit.CropPercent, 1)}
	switch fit.Mode {
	case optimize.FitExact:
		report.Description = "already exactly the display size; shown unchanged"
	case optimize.FitCrop:
		report.Description = fmt.Sprintf("cropped to fill the screen, removing %.1f%% of the picture", report.CropPercent)
	case optimize.FitMat:
		report.Description = "shown whole on a gallery mat because cropping would cut too much or enlarge too far"
	case optimize.FitBlur:
		report.Description = "shown whole on a blurred background because cropping would cut too much or enlarge too far"
	case optimize.FitCollage:
		report.Description = "portrait: paired with another portrait when possible, otherwise shown on a mat"
	case optimize.FitUnchanged:
		report.Description = "optimization is off; the TV receives the file unchanged"
	}
	return report
}

func round(value float64, digits int) float64 {
	scale := math.Pow(10, float64(digits))
	return math.Round(value*scale) / scale
}

// imageBytes returns the image from exactly one of the inputs, bounded by the
// per-image limit.
func (svc *service) imageBytes(ctx context.Context, input addArtworkInput) ([]byte, error) {
	link, encoded := strings.TrimSpace(input.ImageURL), strings.TrimSpace(input.ImageBase64)
	limit := svc.maxImageBytes()
	switch {
	case link != "" && encoded != "":
		return nil, errors.New("provide image_url or image_base64, not both")
	case link != "":
		return svc.fetcher.Fetch(ctx, link, limit)
	case encoded != "":
		return decodeBase64Image(encoded, limit)
	default:
		return nil, errors.New("provide the image as image_url (https) or image_base64")
	}
}

func decodeBase64Image(encoded string, limit int64) ([]byte, error) {
	if strings.HasPrefix(encoded, "data:") {
		header, payload, found := strings.Cut(encoded, ",")
		if !found || !strings.HasSuffix(header, ";base64") {
			return nil, errors.New("image_base64 data URLs must be base64 encoded")
		}
		encoded = payload
	}
	encoded = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == ' ' || character == '\t' {
			return -1
		}
		return character
	}, encoded)
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > limit+2 {
		return nil, fmt.Errorf("the image exceeds the %d-byte limit", limit)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
	}
	if err != nil {
		return nil, errors.New("image_base64 is not valid base64")
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the image exceeds the %d-byte limit", limit)
	}
	if err := sniffImage(data); err != nil {
		return nil, err
	}
	return data, nil
}

// importHint names stored artwork after its title, or its collection.
func importHint(title, slug string) string {
	if hint, err := themes.Slug(title); err == nil {
		return hint
	}
	return slug
}
