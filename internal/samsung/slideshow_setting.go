package samsung

import (
	"fmt"
	"strconv"
	"strings"
)

// SlideshowKind is the Samsung wire mode for ordered or shuffled playback.
type SlideshowKind string

const (
	SlideshowSequential SlideshowKind = "slideshow"
	SlideshowShuffle    SlideshowKind = "shuffleslideshow"
)

// SlideshowSetting is a complete observed or desired slideshow value.
// Interval is zero when the slideshow is disabled.
type SlideshowSetting struct {
	Interval int
	Kind     SlideshowKind
}

func parseSlideshowSetting(value, kind string) (SlideshowSetting, error) {
	setting := SlideshowSetting{Kind: SlideshowKind(strings.TrimSpace(kind))}
	if !setting.Kind.valid() {
		return SlideshowSetting{}, fmt.Errorf("invalid slideshow kind %q", kind)
	}
	value = strings.TrimSpace(value)
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

func (kind SlideshowKind) valid() bool {
	return kind == SlideshowSequential || kind == SlideshowShuffle
}

// Valid reports whether the setting can be observed from or sent to a TV.
func (setting SlideshowSetting) Valid() bool {
	return setting.Interval >= 0 && setting.Kind.valid()
}
