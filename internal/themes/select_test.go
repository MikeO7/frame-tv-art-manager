package themes

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func testItem(name string) collection.Item {
	digest := sha256.Sum256([]byte(name))
	return collection.Item{
		Name: name, Key: name, Digest: digest,
		Origin: collection.Origin{Key: "upload:" + hex.EncodeToString(digest[:]), Class: collection.OriginOperatorUpload},
	}
}

func memberByKey(name string) Member {
	return Member{Key: name}
}

func themed(name string, exclusive bool, activation Activation, members ...string) Collection {
	slug, err := Slug(name)
	if err != nil {
		panic(err)
	}
	result := Collection{Slug: slug, Name: name, Exclusive: exclusive, Activation: activation, Members: []Member{}}
	for _, member := range members {
		result.Members = append(result.Members, memberByKey(member))
	}
	return result
}

func TestSelectSemantics(t *testing.T) {
	t.Parallel()
	december := time.Date(2026, time.December, 20, 12, 0, 0, 0, time.UTC)
	july := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	christmasWindow := Activation{Mode: ModeWindow, Start: "12-01", End: "01-06"}
	items := []collection.Item{testItem("everyday-a.jpg"), testItem("everyday-b.jpg"), testItem("tree.jpg"), testItem("snow.jpg"), testItem("beach.jpg")}
	tests := []struct {
		name          string
		collections   []Collection
		now           time.Time
		want          []string
		wantExclusive bool
		wantActive    []string
	}{
		{
			name: "no collections selects everything", now: december,
			want: []string{"everyday-a.jpg", "everyday-b.jpg", "tree.jpg", "snow.jpg", "beach.jpg"},
		},
		{
			name: "exclusive seasonal collection replaces everyday art while active", now: december,
			collections: []Collection{themed("Christmas", true, christmasWindow, "tree.jpg", "snow.jpg")},
			want:        []string{"tree.jpg", "snow.jpg"}, wantExclusive: true, wantActive: []string{"christmas"},
		},
		{
			name: "inactive seasonal collection is hidden and everyday art returns", now: july,
			collections: []Collection{themed("Christmas", true, christmasWindow, "tree.jpg", "snow.jpg")},
			want:        []string{"everyday-a.jpg", "everyday-b.jpg", "beach.jpg"},
		},
		{
			name: "non-exclusive active collection adds to everyday art", now: july,
			collections: []Collection{themed("Summer", false, Activation{Mode: ModeAlways}, "beach.jpg")},
			want:        []string{"everyday-a.jpg", "everyday-b.jpg", "tree.jpg", "snow.jpg", "beach.jpg"},
			wantActive:  []string{"summer"},
		},
		{
			name: "non-exclusive active collection joins an exclusive takeover", now: december,
			collections: []Collection{
				themed("Christmas", true, christmasWindow, "tree.jpg"),
				themed("Winter", false, Activation{Mode: ModeAlways}, "snow.jpg"),
			},
			want: []string{"tree.jpg", "snow.jpg"}, wantExclusive: true, wantActive: []string{"christmas", "winter"},
		},
		{
			name: "art in an active and an inactive collection is shown", now: july,
			collections: []Collection{
				themed("Christmas", true, christmasWindow, "snow.jpg", "tree.jpg"),
				themed("Winter", false, Activation{Mode: ModeAlways}, "snow.jpg"),
			},
			want:       []string{"everyday-a.jpg", "everyday-b.jpg", "snow.jpg", "beach.jpg"},
			wantActive: []string{"winter"},
		},
		{
			name: "off collection hides its members", now: july,
			collections: []Collection{themed("Draft", true, Activation{Mode: ModeOff}, "beach.jpg")},
			want:        []string{"everyday-a.jpg", "everyday-b.jpg", "tree.jpg", "snow.jpg"},
		},
		{
			name: "active exclusive collection without present art does not empty the TV", now: december,
			collections: []Collection{themed("Christmas", true, christmasWindow, "missing.jpg")},
			want:        []string{"everyday-a.jpg", "everyday-b.jpg", "tree.jpg", "snow.jpg", "beach.jpg"},
			wantActive:  []string{"christmas"},
		},
		{
			name: "empty collections file behaves as before", now: december, collections: []Collection{},
			want: []string{"everyday-a.jpg", "everyday-b.jpg", "tree.jpg", "snow.jpg", "beach.jpg"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := Empty()
			file.Collections = test.collections
			selection := Select(file, items, test.now)
			got := make([]string, 0, len(selection.Items))
			for _, item := range selection.Items {
				got = append(got, item.Name)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("Select() items = %v, want %v", got, test.want)
			}
			if selection.Exclusive != test.wantExclusive {
				t.Fatalf("Select() exclusive = %t, want %t", selection.Exclusive, test.wantExclusive)
			}
			if want := test.wantActive; !slices.Equal(selection.Active, append([]string{}, want...)) {
				t.Fatalf("Select() active = %v, want %v", selection.Active, want)
			}
			if selection.Hidden != len(items)-len(test.want) {
				t.Fatalf("Select() hidden = %d, want %d", selection.Hidden, len(items)-len(test.want))
			}
		})
	}
}

func TestMembershipSurvivesEngineRenamesAndReencodes(t *testing.T) {
	t.Parallel()
	original := testItem("tree.jpg")
	originalDigest := hex.EncodeToString(original.Digest[:])
	optimized := original
	optimized.Name = "tree--a1b2c3d4e5f6.jpg"
	optimized.Digest = sha256.Sum256([]byte("re-encoded"))
	tests := []struct {
		name   string
		member Member
		item   collection.Item
		want   bool
	}{
		{"key survives rename and re-encode", Member{Key: "tree.jpg"}, optimized, true},
		{"key matches case-insensitively", Member{Key: "TREE.JPG"}, optimized, true},
		{"upload origin survives rename and re-encode", Member{Origin: original.Origin.Key}, optimized, true},
		{"digest identifies unchanged bytes", Member{Digest: originalDigest}, original, true},
		{"digest alone does not follow a re-encode", Member{Digest: originalDigest}, optimized, false},
		{"other artwork is not a member", Member{Key: "snow.jpg"}, optimized, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.member.Matches(IdentityOf(test.item)); got != test.want {
				t.Fatalf("Matches() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCollectionMembershipEditing(t *testing.T) {
	t.Parallel()
	christmas, err := NewCollection("Christmas", "Holiday art", true, Activation{Mode: ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	digest := hex.EncodeToString(make([]byte, 32))
	added, err := christmas.AddMember(Member{Origin: "upload:" + digest, Digest: digest, Title: "Tree"})
	if err != nil || !added {
		t.Fatalf("AddMember() = %t, %v, want new member", added, err)
	}
	added, err = christmas.AddMember(Member{Key: "tree.jpg", Origin: "upload:" + digest, Artist: "Model"})
	if err != nil || added {
		t.Fatalf("AddMember(same artwork) = %t, %v, want merge", added, err)
	}
	if len(christmas.Members) != 1 || christmas.Members[0].Key != "tree.jpg" ||
		christmas.Members[0].Title != "Tree" || christmas.Members[0].Artist != "Model" {
		t.Fatalf("merged members = %+v", christmas.Members)
	}
	if _, err := christmas.AddMember(Member{}); err == nil {
		t.Fatal("AddMember() accepted a member without identity")
	}
	if _, err := christmas.AddMember(Member{Digest: "ABC"}); err == nil {
		t.Fatal("AddMember() accepted an invalid digest")
	}
	if _, err := christmas.AddMember(Member{Key: "../escape.jpg"}); err == nil {
		t.Fatal("AddMember() accepted a key with a path")
	}
	if removed := christmas.RemoveMember(Identity{Key: "snow.jpg"}); removed != 0 {
		t.Fatalf("RemoveMember(other) = %d, want 0", removed)
	}
	if removed := christmas.RemoveMember(Identity{Key: "tree.jpg"}); removed != 1 || len(christmas.Members) != 0 {
		t.Fatalf("RemoveMember() = %d, members %+v", removed, christmas.Members)
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"Christmas":                "christmas",
		"  Dutch Winter Landscapes ": "dutch-winter-landscapes",
		"Spring/Easter 2027!":      "spring-easter-2027",
		"Noël":                     "nol",
	} {
		got, err := Slug(name)
		if err != nil || got != want {
			t.Fatalf("Slug(%q) = %q, %v, want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"", "   ", "!!!", "🎄"} {
		if _, err := Slug(name); err == nil {
			t.Fatalf("Slug(%q) succeeded, want an error", name)
		}
	}
}

func TestFileFindAndAdd(t *testing.T) {
	t.Parallel()
	file := Empty()
	christmas, err := NewCollection("Christmas", "", true, Activation{Mode: ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Add(christmas); err != nil {
		t.Fatal(err)
	}
	duplicate, err := NewCollection("CHRISTMAS", "", false, Activation{Mode: ModeAlways})
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Add(duplicate); err == nil {
		t.Fatal("Add() accepted a duplicate slug")
	}
	if index, err := file.Find("christmas"); err != nil || index != 0 {
		t.Fatalf("Find(slug) = %d, %v", index, err)
	}
	if _, err := file.Find("Easter"); err == nil {
		t.Fatal("Find() found a missing collection")
	}
	if _, err := NewCollection("Bad window", "", true, Activation{Mode: ModeWindow, Start: "12-01"}); err == nil {
		t.Fatal("NewCollection() accepted an incomplete window")
	}
}
