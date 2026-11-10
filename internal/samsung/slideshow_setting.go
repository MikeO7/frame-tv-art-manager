package samsung

import (
	"fmt"
	"strconv"
	"strings"
)

// slideshowKind is the Samsung wire mode for ordered or shuffled playback.
type slideshowKind string

const (
	SlideshowSequential slideshowKind = "slideshow"
	SlideshowShuffle    slideshowKind = "shuffleslideshow"
)

// SlideshowSetting is a complete observed or desired slideshow value.
// Interval is zero when the slideshow is disabled. Category is the artwork
// category the TV cycles through (MY-C0002 for uploads, MY-C0004 for
// favorites); it is empty when the TV did not report one.
type SlideshowSetting struct {
	Interval int
	Kind     slideshowKind
	Category string `json:",omitempty"`
}

func parseSlideshowSetting(value, kind, category string) (SlideshowSetting, error) {
	setting := SlideshowSetting{
		Kind: slideshowKind(strings.TrimSpace(kind)), Category: strings.TrimSpace(category),
	}
	value = strings.TrimSpace(value)
	if value == stringOff && setting.Kind == "" {
		// Some firmware omits the playback kind while the slideshow is off.
		setting.Kind = SlideshowShuffle
	}
	if !setting.Kind.valid() {
		return SlideshowSetting{}, fmt.Errorf("invalid slideshow kind %q", kind)
	}
	if value == stringOff {
		return setting, nil
	}
	interval, err := strconv.Atoi(value)
	if err != nil || interval <= 0 {
		return SlideshowSetting{}, fmt.Errorf("invalid slideshow interval %q", value)
	}
	setting.Interval = interval
	return setting, nil
}

func (kind slideshowKind) valid() bool {
	return kind == SlideshowSequential || kind == SlideshowShuffle
}

// Valid reports whether the setting can be observed from or sent to a TV.
func (setting SlideshowSetting) Valid() bool {
	return setting.Interval >= 0 && setting.Kind.valid()
}

// wireCategory is the category sent with a slideshow write: the observed one,
// or the uploaded-art category when the TV never reported one.
func (setting SlideshowSetting) wireCategory() string {
	if setting.Category != "" {
		return setting.Category
	}
	return userArtCategory
}
