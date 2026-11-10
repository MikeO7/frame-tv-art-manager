// Package themes owns themed collections: named groups of artwork from the
// Artwork Collection that are shown only while their activation rule is
// active. Membership and rules live in the collections.json control file.
package themes

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// fileName is the control file inside the Artwork Collection's private
// control directory that stores themed collections.
const fileName = "collections.json"

const (
	fileVersion          = 1
	maxCollections       = 200
	maxMembers           = 10_000
	maxSlugBytes         = 64
	maxNameBytes         = 120
	maxDescriptionBytes  = 2000
	maxAttributionBytes  = 300
	maxIdentityBytes     = 512
	monthDayLayout       = "01-02"
	leapYearForMonthDays = 2024
)

// Mode selects how a collection's activation is decided.
type Mode string

const (
	// ModeAlways keeps the collection active every day.
	ModeAlways Mode = "always"
	// ModeOff keeps the collection inactive; its members are hidden unless
	// another active collection includes them.
	ModeOff Mode = "off"
	// ModeWindow activates the collection between Start and End each year.
	ModeWindow Mode = "window"
)

// Activation is a collection's yearly schedule. Start and End are inclusive
// MM-DD dates; a window whose start is after its end wraps the year end.
type Activation struct {
	Mode  Mode   `json:"mode"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// Member references one artwork by stable identity. Key is the artwork key
// from the collection manifest, Origin the stable Origin Key, and Digest the
// hex SHA-256 of the bytes when the member was added. Any one of them
// identifying an item makes that item a member, so engine-owned renames and
// re-encodes never break membership.
type Member struct {
	Key         string `json:"key,omitempty"`
	Origin      string `json:"origin,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	Description string `json:"description,omitempty"`
	// License and Link credit a public-domain or licensed work, for example
	// one found by a museum search.
	License string    `json:"license,omitempty"`
	Link    string    `json:"link,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// Collection is one named, scheduled group of artwork.
type Collection struct {
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Exclusive   bool       `json:"exclusive"`
	Activation  Activation `json:"activation"`
	Members     []Member   `json:"members"`
}

// File is the complete collections.json document.
type File struct {
	Version     int          `json:"version"`
	Collections []Collection `json:"collections"`
}

// ErrNotFound reports a collection that does not exist.
var ErrNotFound = errors.New("collection not found")

// errExists reports a collection name that is already taken.
var errExists = errors.New("collection already exists")

// empty returns the document used when no collections file exists.
func empty() File {
	return File{Version: fileVersion, Collections: []Collection{}}
}

// Slug derives the stable identifier of a collection name: lowercase letters
// and digits joined by single hyphens.
func Slug(name string) (string, error) {
	var builder strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			if character > unicode.MaxASCII {
				continue
			}
			builder.WriteRune(character)
			separator = false
		case builder.Len() > 0 && !separator:
			builder.WriteByte('-')
			separator = true
		}
		if builder.Len() >= maxSlugBytes {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("collection name %q must contain at least one ASCII letter or digit", name)
	}
	return slug, nil
}

// Find returns the index of the collection named or slugged by reference.
func (file File) Find(reference string) (int, error) {
	slug, err := Slug(reference)
	if err != nil {
		return -1, err
	}
	index := slices.IndexFunc(file.Collections, func(collection Collection) bool { return collection.Slug == slug })
	if index < 0 {
		return -1, fmt.Errorf("%w: %q", ErrNotFound, reference)
	}
	return index, nil
}

// NewCollection validates and returns a new collection without members.
func NewCollection(name, description string, exclusive bool, activation Activation) (Collection, error) {
	slug, err := Slug(name)
	if err != nil {
		return Collection{}, err
	}
	collection := Collection{
		Slug: slug, Name: strings.TrimSpace(name), Description: strings.TrimSpace(description),
		Exclusive: exclusive, Activation: activation, Members: []Member{},
	}
	if err := collection.validate(); err != nil {
		return Collection{}, err
	}
	return collection, nil
}

// Add appends collection, rejecting a duplicate slug.
func (file *File) Add(collection Collection) error {
	if slices.ContainsFunc(file.Collections, func(existing Collection) bool { return existing.Slug == collection.Slug }) {
		return fmt.Errorf("%w: %q", errExists, collection.Name)
	}
	if len(file.Collections) >= maxCollections {
		return fmt.Errorf("at most %d collections are supported", maxCollections)
	}
	file.Collections = append(file.Collections, collection)
	return nil
}

// AddMember records member in the collection, merging it into an existing
// member that identifies the same artwork. It reports whether the member is
// new to the collection.
func (collection *Collection) AddMember(member Member) (bool, error) {
	if err := member.validate(); err != nil {
		return false, err
	}
	for index, existing := range collection.Members {
		if existing.sameArtwork(member) {
			collection.Members[index] = existing.merge(member)
			return false, nil
		}
	}
	if len(collection.Members) >= maxMembers {
		return false, fmt.Errorf("collection %q already has the maximum of %d members", collection.Name, maxMembers)
	}
	collection.Members = append(collection.Members, member)
	return true, nil
}

// RemoveMember drops every member that identifies artwork and reports how
// many were removed. Artwork files are never touched.
func (collection *Collection) RemoveMember(artwork Identity) int {
	before := len(collection.Members)
	collection.Members = slices.DeleteFunc(collection.Members, func(member Member) bool {
		return member.Matches(artwork)
	})
	return before - len(collection.Members)
}

// Identity is the stable identity of one artwork item.
type Identity struct {
	Key    string
	Origin string
	Digest string
}

// Matches reports whether member identifies artwork.
func (member Member) Matches(artwork Identity) bool {
	return (member.Key != "" && strings.EqualFold(member.Key, artwork.Key)) ||
		(member.Origin != "" && member.Origin == artwork.Origin) ||
		(member.Digest != "" && member.Digest == artwork.Digest)
}

func (member Member) sameArtwork(other Member) bool {
	return member.Matches(Identity{Key: other.Key, Origin: other.Origin, Digest: other.Digest})
}

// merge fills identity and attribution gaps in member from update.
func (member Member) merge(update Member) Member {
	member.Key = firstNonEmpty(update.Key, member.Key)
	member.Origin = firstNonEmpty(member.Origin, update.Origin)
	member.Digest = firstNonEmpty(member.Digest, update.Digest)
	member.Name = firstNonEmpty(update.Name, member.Name)
	member.Title = firstNonEmpty(update.Title, member.Title)
	member.Artist = firstNonEmpty(update.Artist, member.Artist)
	member.Description = firstNonEmpty(update.Description, member.Description)
	member.License = firstNonEmpty(update.License, member.License)
	member.Link = firstNonEmpty(update.Link, member.Link)
	return member
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (file File) validate() error {
	if file.Version != fileVersion {
		return fmt.Errorf("unsupported collections file version %d", file.Version)
	}
	if len(file.Collections) > maxCollections {
		return fmt.Errorf("collections file has more than %d collections", maxCollections)
	}
	seen := make(map[string]struct{}, len(file.Collections))
	for _, collection := range file.Collections {
		if err := collection.validate(); err != nil {
			return err
		}
		if _, duplicate := seen[collection.Slug]; duplicate {
			return fmt.Errorf("collection slug %q is duplicated", collection.Slug)
		}
		seen[collection.Slug] = struct{}{}
	}
	return nil
}

func (collection Collection) validate() error {
	slug, err := Slug(collection.Name)
	if err != nil {
		return err
	}
	if collection.Slug != slug {
		return fmt.Errorf("collection %q slug %q does not match its name", collection.Name, collection.Slug)
	}
	if err := checkText("collection name", collection.Name, maxNameBytes); err != nil {
		return err
	}
	if err := checkText("collection description", collection.Description, maxDescriptionBytes); err != nil {
		return err
	}
	if err := collection.Activation.Validate(); err != nil {
		return fmt.Errorf("collection %q: %w", collection.Name, err)
	}
	if collection.Members == nil {
		return fmt.Errorf("collection %q members must be a list", collection.Name)
	}
	if len(collection.Members) > maxMembers {
		return fmt.Errorf("collection %q has more than %d members", collection.Name, maxMembers)
	}
	for _, member := range collection.Members {
		if err := member.validate(); err != nil {
			return fmt.Errorf("collection %q: %w", collection.Name, err)
		}
	}
	return nil
}

func (member Member) validate() error {
	if member.Key == "" && member.Origin == "" && member.Digest == "" {
		return errors.New("member needs a key, origin, or digest")
	}
	for _, field := range []struct {
		label, value string
		limit        int
	}{
		{"member key", member.Key, maxIdentityBytes},
		{"member origin", member.Origin, maxIdentityBytes},
		{"member name", member.Name, maxIdentityBytes},
		{"member title", member.Title, maxAttributionBytes},
		{"member artist", member.Artist, maxAttributionBytes},
		{"member description", member.Description, maxDescriptionBytes},
		{"member license", member.License, maxAttributionBytes},
		{"member link", member.Link, maxIdentityBytes},
	} {
		if err := checkText(field.label, field.value, field.limit); err != nil {
			return err
		}
	}
	if err := ValidateLink(member.Link); err != nil {
		return err
	}
	if member.Key != "" && strings.ContainsAny(member.Key, `/\`) {
		return fmt.Errorf("member key %q must be a single filename", member.Key)
	}
	if member.Digest != "" {
		decoded, err := hex.DecodeString(member.Digest)
		if err != nil || len(decoded) != 32 || member.Digest != strings.ToLower(member.Digest) {
			return fmt.Errorf("member digest %q must be a lowercase hex SHA-256", member.Digest)
		}
	}
	return nil
}

func checkText(label, value string, limit int) error {
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	if !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be trimmed UTF-8 text", label)
	}
	if strings.ContainsFunc(value, func(character rune) bool {
		return unicode.IsControl(character) && character != '\n'
	}) {
		return fmt.Errorf("%s contains control characters", label)
	}
	return nil
}

// ValidateLink accepts an empty link or an absolute http or https URL. Links
// are attribution only; the manager never fetches them.
func ValidateLink(link string) error {
	if link == "" {
		return nil
	}
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("member link %q must be an http or https web address", link)
	}
	return nil
}
