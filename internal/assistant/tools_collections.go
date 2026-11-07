package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

type activationInput struct {
	Mode  string `json:"mode" jsonschema:"always, off, or window"`
	Start string `json:"start,omitempty" jsonschema:"first day shown each year as MM-DD, required for window"`
	End   string `json:"end,omitempty" jsonschema:"last day shown each year as MM-DD, required for window; earlier than start wraps the new year"`
}

type createCollectionInput struct {
	Name        string           `json:"name" jsonschema:"collection name, for example Christmas"`
	Description string           `json:"description,omitempty"`
	Exclusive   *bool            `json:"exclusive,omitempty" jsonschema:"while active, replace everyday art instead of adding to it (default true)"`
	Activation  *activationInput `json:"activation,omitempty" jsonschema:"when the collection is shown (default off)"`
}

type updateCollectionInput struct {
	Collection  string           `json:"collection" jsonschema:"collection name or slug"`
	Description *string          `json:"description,omitempty" jsonschema:"new description"`
	Exclusive   *bool            `json:"exclusive,omitempty" jsonschema:"true replaces everyday art while active; false adds to it"`
	Activation  *activationInput `json:"activation,omitempty" jsonschema:"new schedule"`
}

type collectionReferenceInput struct {
	Collection string `json:"collection" jsonschema:"collection name or slug"`
}

type removeFromCollectionInput struct {
	Collection string `json:"collection" jsonschema:"collection name or slug"`
	Artwork    string `json:"artwork" jsonschema:"filename, artwork key, or digest"`
}

type collectionSummary struct {
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Exclusive      bool            `json:"exclusive"`
	Activation     activationInput `json:"activation"`
	Schedule       string          `json:"schedule"`
	ActiveToday    bool            `json:"active_today"`
	Members        int             `json:"members" jsonschema:"recorded members"`
	PresentMembers int             `json:"present_members" jsonschema:"members whose image is in the artwork folder"`
}

type listCollectionsOutput struct {
	Today         string              `json:"today" jsonschema:"today's date in LOCATION_TIMEZONE"`
	Timezone      string              `json:"timezone"`
	Collections   []collectionSummary `json:"collections"`
	ActiveToday   []string            `json:"active_today"`
	EverydayShown bool                `json:"everyday_shown" jsonschema:"false while an active exclusive collection replaces everyday art"`
	Selected      int                 `json:"selected" jsonschema:"artwork chosen for the TV today"`
	Total         int                 `json:"total" jsonschema:"artwork in the folder"`
}

type collectionChangeOutput struct {
	Collection collectionSummary `json:"collection"`
	Message    string            `json:"message"`
}

type removeOutput struct {
	Collection string `json:"collection"`
	Removed    int    `json:"removed"`
	Message    string `json:"message"`
}

func (svc *service) registerCollectionTools(server *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(false)}
	mcp.AddTool(server, &mcp.Tool{
		Name:  "list_collections",
		Title: "List themed collections",
		Description: "Lists every themed collection with its schedule, whether it is active today, and how many of its " +
			"pictures are in the folder, plus what today's rules select for the TV.",
		Annotations: readOnly,
	}, svc.listCollections)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "create_collection",
		Title: "Create a themed collection",
		Description: "Creates an empty themed collection. It is off by default; pass activation to schedule it. " +
			"exclusive (default true) means that while it is active only active collections are shown on the TV.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)},
	}, svc.createCollection)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "update_collection",
		Title: "Schedule or change a themed collection",
		Description: "Changes a collection's description, schedule, or exclusive flag. Schedules: mode \"always\"; " +
			"mode \"off\" (hidden); or mode \"window\" with start and end as MM-DD, inclusive, evaluated each year in " +
			"the TV's time zone (12-01 to 01-06 runs over the new year). Call sync_now afterwards to apply it.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)},
	}, svc.updateCollection)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "remove_from_collection",
		Title: "Remove a picture from a collection",
		Description: "Removes one picture from a collection's membership. The image file is kept; if it belongs to " +
			"no other collection it becomes everyday art.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)},
	}, svc.removeFromCollection)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "delete_collection",
		Title: "Delete a themed collection",
		Description: "Deletes a collection and its membership list. Image files are never deleted; pictures that " +
			"belonged only to this collection become everyday art.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)},
	}, svc.deleteCollection)
}

func (svc *service) listCollections(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ emptyInput,
) (*mcp.CallToolResult, listCollectionsOutput, error) {
	file, err := svc.themes.Load(ctx)
	if err != nil {
		return nil, listCollectionsOutput{}, err
	}
	snapshot, err := svc.inventory(ctx)
	if err != nil {
		return nil, listCollectionsOutput{}, err
	}
	today := svc.today()
	selection := themes.Select(file, snapshot.Items, today)
	output := listCollectionsOutput{
		Today: today.Format("2006-01-02"), Timezone: svc.location.String(),
		Collections: make([]collectionSummary, 0, len(file.Collections)), ActiveToday: selection.Active,
		EverydayShown: !selection.Exclusive, Selected: len(selection.Items), Total: len(snapshot.Items),
	}
	for _, themed := range file.Collections {
		output.Collections = append(output.Collections, svc.summarizeCollection(themed, snapshot.Items))
	}
	return nil, output, nil
}

func (svc *service) summarizeCollection(themed themes.Collection, items []collection.Item) collectionSummary {
	present := 0
	for _, item := range items {
		identity := themes.IdentityOf(item)
		if slices.ContainsFunc(themed.Members, func(member themes.Member) bool { return member.Matches(identity) }) {
			present++
		}
	}
	return collectionSummary{
		Slug: themed.Slug, Name: themed.Name, Description: themed.Description, Exclusive: themed.Exclusive,
		Activation: activationInput{Mode: string(themed.Activation.Mode), Start: themed.Activation.Start, End: themed.Activation.End},
		Schedule:   themed.Activation.Describe(), ActiveToday: themed.Activation.ActiveAt(svc.today()),
		Members: len(themed.Members), PresentMembers: present,
	}
}

func (svc *service) createCollection(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input createCollectionInput,
) (*mcp.CallToolResult, collectionChangeOutput, error) {
	if err := svc.rejectDryRun(); err != nil {
		return nil, collectionChangeOutput{}, err
	}
	activation := themes.Activation{Mode: themes.ModeOff}
	if input.Activation != nil {
		activation = input.Activation.rule()
	}
	exclusive := input.Exclusive == nil || *input.Exclusive
	created, err := themes.NewCollection(input.Name, input.Description, exclusive, activation)
	if err != nil {
		return nil, collectionChangeOutput{}, err
	}
	if _, err := svc.themes.Update(ctx, func(file *themes.File) error { return file.Add(created) }); err != nil {
		return nil, collectionChangeOutput{}, err
	}
	svc.logger.Info("themed collection created", "collection", created.Slug, "activation", created.Activation.Mode)
	return nil, collectionChangeOutput{
		Collection: svc.summarizeCollection(created, nil),
		Message:    fmt.Sprintf("Created %q (%s). Add pictures with add_artwork.", created.Name, created.Activation.Describe()),
	}, nil
}

func (input activationInput) rule() themes.Activation {
	return themes.Activation{
		Mode: themes.Mode(strings.ToLower(strings.TrimSpace(input.Mode))), Start: strings.TrimSpace(input.Start),
		End: strings.TrimSpace(input.End),
	}
}

func (svc *service) updateCollection(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input updateCollectionInput,
) (*mcp.CallToolResult, collectionChangeOutput, error) {
	if err := svc.rejectDryRun(); err != nil {
		return nil, collectionChangeOutput{}, err
	}
	if input.Description == nil && input.Exclusive == nil && input.Activation == nil {
		return nil, collectionChangeOutput{}, errors.New("nothing to change: pass description, exclusive, or activation")
	}
	var updated themes.Collection
	_, err := svc.themes.Update(ctx, func(file *themes.File) error {
		index, findErr := file.Find(input.Collection)
		if findErr != nil {
			return findErr
		}
		target := &file.Collections[index]
		if input.Description != nil {
			target.Description = strings.TrimSpace(*input.Description)
		}
		if input.Exclusive != nil {
			target.Exclusive = *input.Exclusive
		}
		if input.Activation != nil {
			rule := input.Activation.rule()
			if validateErr := rule.Validate(); validateErr != nil {
				return validateErr
			}
			target.Activation = rule
		}
		updated = *target
		return nil
	})
	if err != nil {
		return nil, collectionChangeOutput{}, err
	}
	snapshot, err := svc.inventory(ctx)
	if err != nil {
		return nil, collectionChangeOutput{}, err
	}
	svc.logger.Info("themed collection updated", "collection", updated.Slug,
		"activation", updated.Activation.Mode, "exclusive", updated.Exclusive)
	return nil, collectionChangeOutput{
		Collection: svc.summarizeCollection(updated, snapshot.Items),
		Message:    fmt.Sprintf("Updated %q: %s. Call sync_now to apply it to the TV.", updated.Name, updated.Activation.Describe()),
	}, nil
}

func (svc *service) removeFromCollection(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input removeFromCollectionInput,
) (*mcp.CallToolResult, removeOutput, error) {
	if err := svc.rejectDryRun(); err != nil {
		return nil, removeOutput{}, err
	}
	reference := strings.TrimSpace(input.Artwork)
	if reference == "" {
		return nil, removeOutput{}, errors.New("name the artwork by filename, key, or digest")
	}
	snapshot, err := svc.inventory(ctx)
	if err != nil {
		return nil, removeOutput{}, err
	}
	identity := themes.Identity{Key: reference, Digest: strings.ToLower(reference)}
	if item, findErr := findItem(snapshot.Items, reference); findErr == nil {
		identity = themes.IdentityOf(item)
	}
	var slug string
	removed := 0
	_, err = svc.themes.Update(ctx, func(file *themes.File) error {
		index, findErr := file.Find(input.Collection)
		if findErr != nil {
			return findErr
		}
		slug = file.Collections[index].Slug
		removed = file.Collections[index].RemoveMember(identity)
		if removed == 0 {
			return fmt.Errorf("%q is not in collection %q", reference, slug)
		}
		return nil
	})
	if err != nil {
		return nil, removeOutput{}, err
	}
	svc.logger.Info("artwork removed from themed collection", "collection", slug, "removed", removed)
	return nil, removeOutput{
		Collection: slug, Removed: removed,
		Message: "Removed from the collection; the image file is kept. Call sync_now to apply it to the TV.",
	}, nil
}

func (svc *service) deleteCollection(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input collectionReferenceInput,
) (*mcp.CallToolResult, removeOutput, error) {
	if err := svc.rejectDryRun(); err != nil {
		return nil, removeOutput{}, err
	}
	var deleted themes.Collection
	_, err := svc.themes.Update(ctx, func(file *themes.File) error {
		index, findErr := file.Find(input.Collection)
		if findErr != nil {
			return findErr
		}
		deleted = file.Collections[index]
		file.Collections = slices.Delete(file.Collections, index, index+1)
		return nil
	})
	if err != nil {
		return nil, removeOutput{}, err
	}
	svc.logger.Info("themed collection deleted", "collection", deleted.Slug, "members", len(deleted.Members))
	return nil, removeOutput{
		Collection: deleted.Slug, Removed: len(deleted.Members),
		Message: "Deleted the collection. No image files were deleted; pictures in no other collection are now everyday art. " +
			"Call sync_now to apply it to the TV.",
	}, nil
}
