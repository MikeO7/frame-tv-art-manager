package themes

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// membershipsByComparison is the definition Memberships must agree with: an
// item belongs to every collection one of whose members matches it.
func membershipsByComparison(file File, items []collection.Item) [][]string {
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

func TestMembershipsMatchesEveryMemberComparison(t *testing.T) {
	t.Parallel()
	// Names that differ only by case, including the two letters whose simple
	// case folding reaches a third rune: K with the Kelvin sign and s with
	// the long s.
	names := []string{
		"Sunrise.jpg", "SUNRISE.JPG", "sunrise.jpg", "Kelp.jpg", "Kelp.jpg", "kelp.jpg",
		"Moss.png", "Moſs.png", "MOSS.PNG", "Straße.jpg", "STRASSE.jpg", "other.jpg", "bad\xff.jpg", "bad\xfe.jpg",
	}
	items := make([]collection.Item, 0, len(names)+1)
	for _, name := range names {
		items = append(items, testItem(name))
	}
	// An item without a key matches by origin or digest only.
	keyless := testItem("keyless.jpg")
	keyless.Key = ""
	items = append(items, keyless)

	var file File
	for index := range 6 {
		themed := Collection{Slug: fmt.Sprintf("collection-%d", index), Members: []Member{{}}}
		for position, item := range items {
			identity := IdentityOf(item)
			switch (position + index) % 7 {
			case 0:
				themed.Members = append(themed.Members, Member{Key: strings.ToUpper(identity.Key)})
			case 1:
				themed.Members = append(themed.Members, Member{Origin: identity.Origin})
			case 2:
				themed.Members = append(themed.Members, Member{Digest: identity.Digest})
			case 3:
				// A key of other artwork with this artwork's digest, twice.
				member := Member{Key: "renamed-" + identity.Key, Digest: identity.Digest}
				themed.Members = append(themed.Members, member, member)
			}
		}
		file.Collections = append(file.Collections, themed)
	}

	got, want := file.Memberships(items), membershipsByComparison(file, items)
	for index := range items {
		if got[index] == nil || !slices.Equal(got[index], want[index]) {
			t.Errorf("Memberships()[%q] = %v, want %v", items[index].Name, got[index], want[index])
		}
	}
	if matched := slices.ContainsFunc(want, func(slugs []string) bool { return len(slugs) > 2 }); !matched {
		t.Fatal("the fixture matched no item to several collections")
	}
	if got := (File{}).Memberships(items[:1]); len(got) != 1 || got[0] == nil || len(got[0]) != 0 {
		t.Fatalf("Memberships() with no collections = %v, want one empty list", got)
	}
}

func TestFoldKeyAgreesWithEqualFold(t *testing.T) {
	t.Parallel()
	values := []string{"", "a", "A", "k", "K", "K", "s", "S", "ſ", "ß", "SS", "é", "É", "e", "\xff", "\xfe", "ǆ", "ǅ", "Ǆ"}
	for _, left := range values {
		for _, right := range values {
			if got, want := foldKey(left) == foldKey(right), strings.EqualFold(left, right); got != want {
				t.Errorf("foldKey(%q) == foldKey(%q) is %v, strings.EqualFold says %v", left, right, got, want)
			}
		}
	}
}

func BenchmarkMemberships(b *testing.B) {
	items := make([]collection.Item, 0, 2000)
	for index := range 2000 {
		items = append(items, testItem(fmt.Sprintf("artwork-%04d.jpg", index)))
	}
	var file File
	for index := range 20 {
		themed := Collection{Slug: fmt.Sprintf("collection-%d", index)}
		for position := index; position < len(items); position += 7 {
			themed.Members = append(themed.Members, Member{Key: items[position].Key})
		}
		file.Collections = append(file.Collections, themed)
	}
	for b.Loop() {
		file.Memberships(items)
	}
}
