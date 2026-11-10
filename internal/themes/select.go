package themes

import (
	"encoding/hex"
	"slices"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// selection is the artwork chosen for display on one calendar day.
type selection struct {
	// Items is the selected subset, in the snapshot's order.
	Items []collection.Item
	// Active lists the slugs of collections active today, in file order.
	Active []string
	// Exclusive reports that an active exclusive collection with at least one
	// present member replaced the everyday pool.
	Exclusive bool
	// Hidden counts artwork left out of the selection.
	Hidden int
}

// IdentityOf returns the stable identity of a collection item.
func IdentityOf(item collection.Item) Identity {
	return Identity{Key: item.Key, Origin: item.Origin.Key, Digest: hex.EncodeToString(item.Digest[:])}
}

// Memberships returns, for every item, the slugs of the collections that
// include it, in file order. Items in no collection map to an empty list.
func (file File) Memberships(items []collection.Item) [][]string {
	result := make([][]string, len(items))
	for index, item := range items {
		identity := IdentityOf(item)
		result[index] = []string{}
		for _, themed := range file.Collections {
			if slices.ContainsFunc(themed.Members, func(member Member) bool { return member.Matches(identity) }) {
				result[index] = append(result[index], themed.Slug)
			}
		}
	}
	return result
}

// Select chooses the artwork to display on now's calendar day:
//
//   - With no collections, or none active and none holding artwork, every
//     item is selected exactly as if themed collections did not exist.
//   - Artwork in no collection is the everyday pool.
//   - Artwork whose only collections are inactive is hidden.
//   - When an active exclusive collection has at least one item present, only
//     members of active collections are selected.
//   - Otherwise the everyday pool plus members of active collections is
//     selected.
func Select(file File, items []collection.Item, now time.Time) selection {
	active := make(map[string]bool, len(file.Collections))
	selection := selection{Active: []string{}}
	for _, themed := range file.Collections {
		if themed.Activation.ActiveAt(now) {
			active[themed.Slug] = true
			selection.Active = append(selection.Active, themed.Slug)
		}
	}
	memberships := file.Memberships(items)
	exclusive := make(map[string]bool, len(file.Collections))
	for _, themed := range file.Collections {
		exclusive[themed.Slug] = themed.Exclusive
	}
	for _, slugs := range memberships {
		if slices.ContainsFunc(slugs, func(slug string) bool { return active[slug] && exclusive[slug] }) {
			selection.Exclusive = true
			break
		}
	}
	selection.Items = make([]collection.Item, 0, len(items))
	for index, item := range items {
		slugs := memberships[index]
		inActive := slices.ContainsFunc(slugs, func(slug string) bool { return active[slug] })
		everyday := len(slugs) == 0
		if inActive || (everyday && !selection.Exclusive) {
			selection.Items = append(selection.Items, item)
		}
	}
	selection.Hidden = len(items) - len(selection.Items)
	return selection
}
