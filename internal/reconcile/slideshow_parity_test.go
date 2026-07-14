package reconcile

import (
	"encoding/json"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestSlideshowKindChangesPolicyFingerprintAndPlanIntent(t *testing.T) {
	t.Parallel()

	sequential := Policy{Slideshow: SlideshowPolicy{
		Mode: PolicySet, Setting: samsung.SlideshowSetting{Interval: 30, Kind: samsung.SlideshowSequential},
	}}
	shuffle := Policy{Slideshow: SlideshowPolicy{
		Mode: PolicySet, Setting: samsung.SlideshowSetting{Interval: 30, Kind: samsung.SlideshowShuffle},
	}}
	sequentialFingerprint, err := fingerprintPolicy(sequential)
	if err != nil {
		t.Fatalf("fingerprint sequential policy: %v", err)
	}
	shuffleFingerprint, err := fingerprintPolicy(shuffle)
	if err != nil {
		t.Fatalf("fingerprint shuffle policy: %v", err)
	}
	if sequentialFingerprint == shuffleFingerprint {
		t.Fatal("slideshow kind was omitted from the policy fingerprint")
	}

	observation := knownObservation(samsung.PowerStateOn)
	observation.Slideshow = samsung.SlideshowObservation{
		Setting: samsung.SlideshowSetting{Interval: 15, Kind: samsung.SlideshowShuffle},
		Known:   true, ObservedAt: testTime,
	}
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatalf("observation identity: %v", err)
	}
	plan, err := BuildPlan(snapshot(), observation, initialState(identity), sequential)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Commands) != 1 || plan.Commands[0].Kind != CommandSlideshow {
		t.Fatalf("commands = %#v, want one slideshow command", plan.Commands)
	}
	wantPrevious := samsung.SlideshowSetting{Interval: 15, Kind: samsung.SlideshowShuffle}
	wantDesired := samsung.SlideshowSetting{Interval: 30, Kind: samsung.SlideshowSequential}
	if plan.Commands[0].PreviousSlideshow == nil || *plan.Commands[0].PreviousSlideshow != wantPrevious ||
		plan.Commands[0].DesiredSlideshow == nil || *plan.Commands[0].DesiredSlideshow != wantDesired {
		t.Fatalf("slideshow intent = %#v, want previous %+v and desired %+v", plan.Commands[0], wantPrevious, wantDesired)
	}
	durable, err := json.Marshal(plan.Commands[0])
	if err != nil {
		t.Fatalf("marshal slideshow intent: %v", err)
	}
	var restored CommandIntent
	if err := json.Unmarshal(durable, &restored); err != nil {
		t.Fatalf("unmarshal slideshow intent: %v", err)
	}
	if !sameIntent(plan.Commands[0], restored) {
		t.Fatalf("durable slideshow intent lost parity: before %#v, after %#v", plan.Commands[0], restored)
	}
}
