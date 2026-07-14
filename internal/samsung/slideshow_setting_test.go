package samsung

import "testing"

func TestParseSlideshowSettingPreservesIntervalAndKind(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		kind      string
		want      SlideshowSetting
		wantError bool
	}{
		{name: "sequential", value: "15", kind: "slideshow", want: SlideshowSetting{Interval: 15, Kind: SlideshowSequential}},
		{name: "shuffle", value: "60", kind: "shuffleslideshow", want: SlideshowSetting{Interval: 60, Kind: SlideshowShuffle}},
		{name: "disabled", value: "off", kind: "shuffleslideshow", want: SlideshowSetting{Kind: SlideshowShuffle}},
		{name: "missing kind", value: "15", wantError: true},
		{name: "unknown kind", value: "15", kind: "random", wantError: true},
		{name: "invalid interval", value: "later", kind: "slideshow", wantError: true},
		{name: "zero interval", value: "0", kind: "slideshow", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSlideshowSetting(test.value, test.kind)
			if test.wantError {
				if err == nil {
					t.Fatalf("parseSlideshowSetting() = %+v, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("parseSlideshowSetting() = %+v, %v, want %+v", got, err, test.want)
			}
		})
	}
}
