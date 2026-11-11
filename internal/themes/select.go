package themes

import (
	"encoding/hex"
	"slices"
	"strings"
	"time"
	"unicode"

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
// An item belongs to a collection when any member matches it, exactly as
// Member.Matches decides.
func (file File) Memberships(items []collection.Item) [][]string {
	index := file.membershipIndex()
	result := make([][]string, len(items))
	var found []int
	for position, item := range items {
		identity := IdentityOf(item)
		found = append(found[:0], index.keys[foldKey(identity.Key)]...)
		found = append(found, index.origins[identity.Origin]...)
		found = append(found, index.digests[identity.Digest]...)
		slices.Sort(found)
		slugs := make([]string, 0, len(found))
		for _, collectionIndex := range slices.Compact(found) {
			slugs = append(slugs, file.Collections[collectionIndex].Slug)
		}
		result[position] = slugs
	}
	return result
}

// membershipIndex maps every identity a member can match on to the positions,
// in file order, of the collections holding such a member. It is built once
// per call so matching an item costs three lookups instead of a comparison
// with every member of every collection.
type membershipIndex struct {
	keys, origins, digests map[string][]int
}

func (file File) membershipIndex() membershipIndex {
	index := membershipIndex{keys: map[string][]int{}, origins: map[string][]int{}, digests: map[string][]int{}}
	add := func(lookup map[string][]int, value string, collectionIndex int) {
		// An empty field matches nothing, and one collection is listed once.
		if value == "" || slices.Contains(lookup[value], collectionIndex) {
			return
		}
		lookup[value] = append(lookup[value], collectionIndex)
	}
	for collectionIndex, themed := range file.Collections {
		for _, member := range themed.Members {
			add(index.keys, foldKey(member.Key), collectionIndex)
			add(index.origins, member.Origin, collectionIndex)
			add(index.digests, member.Digest, collectionIndex)
		}
	}
	return index
}

// foldKey returns a string equal for exactly the strings strings.EqualFold
// treats as equal: every rune is replaced by the smallest rune of its simple
// case-folding orbit.
func foldKey(value string) string {
	return strings.Map(func(char rune) rune {
		smallest := char
		for folded := unicode.SimpleFold(char); folded != char; folded = unicode.SimpleFold(folded) {
			smallest = min(smallest, folded)
		}
		return smallest
	}, value)
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
