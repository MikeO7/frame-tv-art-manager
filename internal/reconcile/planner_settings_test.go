package reconcile

import (
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestBuildPlanFitsBrightnessToTheTVScale(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		observed samsung.SettingObservation
		policy   int
		want     *int
	}{
		{name: "within scale", observed: samsung.SettingObservation{Value: 2, Max: 10}, policy: 7, want: new(7)},
		{name: "above maximum", observed: samsung.SettingObservation{Value: 2, Max: 10}, policy: 40, want: new(10)},
		{name: "below minimum", observed: samsung.SettingObservation{Value: 5, Min: 1, Max: 10}, policy: 0, want: new(1)},
		{name: "already at the clamped level", observed: samsung.SettingObservation{Value: 10, Max: 10}, policy: 50},
		{name: "scale unknown", observed: samsung.SettingObservation{Value: 2}, policy: 40, want: new(40)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := knownObservation(samsung.PowerStateOn)
			observation.Brightness = test.observed
			observation.Brightness.Known, observation.Brightness.ObservedAt = true, testTime
			observation.Capabilities.BrightnessRead = samsung.SupportSupported
			observation.Capabilities.BrightnessWrite = samsung.SupportSupported
			identity, err := identityFromObservation(observation)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := BuildPlan(snapshot(), observation, initialState(identity), Policy{
				Brightness: SettingPolicy{Mode: PolicySet, Value: test.policy},
			})
			if err != nil {
				t.Fatalf("BuildPlan() error = %v", err)
			}
			var got *int
			for _, command := range plan.Commands {
				if command.Kind == CommandBrightness {
					got = command.DesiredValue
				}
			}
			if (got == nil) != (test.want == nil) || (got != nil && *got != *test.want) {
				t.Fatalf("desired brightness = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBuildPlanKeepsTheSlideshowCategoryTheTVPlays(t *testing.T) {
	t.Parallel()
	observation := knownObservation(samsung.PowerStateOn)
	observation.Slideshow = samsung.SlideshowObservation{
		Setting: samsung.SlideshowSetting{Interval: 15, Kind: samsung.SlideshowShuffle, Category: "MY-C0004"},
		Known:   true, ObservedAt: testTime,
	}
	observation.Capabilities.SlideshowRead = samsung.SupportSupported
	observation.Capabilities.SlideshowWrite = samsung.SupportSupported
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		setting  samsung.SlideshowSetting
		wantPlan bool
	}{
		{name: "same interval and kind", setting: samsung.SlideshowSetting{Interval: 15, Kind: samsung.SlideshowShuffle}},
		{name: "new interval", setting: samsung.SlideshowSetting{Interval: 60, Kind: samsung.SlideshowShuffle}, wantPlan: true},
	} {
		plan, err := BuildPlan(snapshot(), observation, initialState(identity), Policy{
			Slideshow: SlideshowPolicy{Mode: PolicySet, Setting: test.setting},
		})
		if err != nil {
			t.Fatalf("%s: BuildPlan() error = %v", test.name, err)
		}
		if (len(plan.Commands) == 1) != test.wantPlan {
			t.Fatalf("%s: commands = %+v", test.name, plan.Commands)
		}
		if test.wantPlan && plan.Commands[0].DesiredSlideshow.Category != "MY-C0004" {
			t.Fatalf("%s: desired = %+v, want the observed category kept", test.name, plan.Commands[0].DesiredSlideshow)
		}
	}
}
