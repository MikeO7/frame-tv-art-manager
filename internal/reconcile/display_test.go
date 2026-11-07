package reconcile

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// showLibrary returns count bound pictures, named from a.jpg onward, each on
// the TV under the content ID "id-" plus its name.
func showLibrary(t *testing.T, count int) (collection.Snapshot, samsung.Observation, State) {
	t.Helper()
	items := make([]collection.Item, 0, count)
	ids := make([]string, 0, count)
	for index := range count {
		name := string(rune('a'+index)) + ".jpg"
		items = append(items, artworkItem(name, name))
		ids = append(ids, "id-"+name)
	}
	snap := snapshot(items...)
	observation := knownObservation(samsung.PowerStateOn, ids...)
	observation.Capabilities.ImageSelection = samsung.SupportSupported
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	for _, item := range items {
		digest := hex.EncodeToString(item.Digest[:])
		state.Bindings[digest] = Binding{
			Digest: digest, ContentID: "id-" + item.Name, Name: item.Name,
			CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
		}
	}
	return snap, observation, state
}

func selectCommands(commands []CommandIntent) []CommandIntent {
	return slices.DeleteFunc(slices.Clone(commands), func(command CommandIntent) bool {
		return command.Kind != CommandSelect
	})
}

// showSequence plans and applies count picture changes, one interval apart,
// and returns the content IDs shown in order.
func showSequence(t *testing.T, count int, library int, order ShowOrder) []string {
	t.Helper()
	snap, observation, state := showLibrary(t, library)
	policy := Policy{Show: ShowPolicy{Interval: 15 * time.Minute, Order: order}}
	shown := make([]string, 0, count)
	for step := range count {
		observation.ObservedAt = testTime.Add(time.Duration(step) * 15 * time.Minute)
		plan, err := BuildPlan(snap, observation, state, policy)
		if err != nil {
			t.Fatal(err)
		}
		selects := selectCommands(plan.Commands)
		if len(selects) != 1 {
			t.Fatalf("step %d planned %d picture changes, want 1", step, len(selects))
		}
		recordDisplay(&state, selects[0], observation.ObservedAt)
		observation.CurrentArtwork = selects[0].ContentID
		shown = append(shown, selects[0].ContentID)
	}
	return shown
}

func TestPlanShowShufflesEveryPictureBeforeRepeating(t *testing.T) {
	const library = 7
	shown := showSequence(t, 3*library, library, ShowShuffle)
	var rounds [][]string
	for start := 0; start < len(shown); start += library {
		round := slices.Clone(shown[start : start+library])
		rounds = append(rounds, slices.Clone(round))
		slices.Sort(round)
		if len(slices.Compact(round)) != library {
			t.Fatalf("round %d repeated a picture: %v", len(rounds), shown[start:start+library])
		}
	}
	for index := 1; index < len(shown); index++ {
		if shown[index] == shown[index-1] {
			t.Fatalf("picture %q shown twice in a row: %v", shown[index], shown)
		}
	}
	if slices.Equal(rounds[0], rounds[1]) && slices.Equal(rounds[1], rounds[2]) {
		t.Fatalf("every shuffle round used the same order: %v", rounds[0])
	}
}

func TestPlanShowSequentialFollowsNameOrderAndWraps(t *testing.T) {
	shown := showSequence(t, 5, 3, ShowSequential)
	want := []string{"id-a.jpg", "id-b.jpg", "id-c.jpg", "id-a.jpg", "id-b.jpg"}
	if !slices.Equal(shown, want) {
		t.Fatalf("shown = %v, want %v", shown, want)
	}
}

func TestPlanShowDecidesWhenAChangeIsDue(t *testing.T) {
	interval := 15 * time.Minute
	tests := []struct {
		name    string
		mutate  func(*State, *samsung.Observation)
		policy  ShowPolicy
		wantDue bool
	}{
		{name: "first run changes at once", policy: ShowPolicy{Interval: interval}, wantDue: true},
		{name: "disabled never changes", policy: ShowPolicy{}},
		{
			name:   "too soon",
			policy: ShowPolicy{Interval: interval},
			mutate: func(state *State, observation *samsung.Observation) {
				state.Display = &DisplayState{Digest: digestHex("a"), At: observation.ObservedAt.Add(-5 * time.Minute), Round: 1}
			},
		},
		{
			name:   "a timer firing slightly early still counts",
			policy: ShowPolicy{Interval: interval},
			mutate: func(state *State, observation *samsung.Observation) {
				state.Display = &DisplayState{Digest: digestHex("a"), At: observation.ObservedAt.Add(-14 * time.Minute), Round: 1}
			},
			wantDue: true,
		},
		{
			name:   "unverifiable selection never changes",
			policy: ShowPolicy{Interval: interval},
			mutate: func(_ *State, observation *samsung.Observation) {
				observation.Capabilities.ImageSelection = samsung.SupportUnknown
			},
		},
		{
			name:   "nothing of ours on the TV",
			policy: ShowPolicy{Interval: interval},
			mutate: func(_ *State, observation *samsung.Observation) {
				*observation = knownObservation(samsung.PowerStateOn)
				observation.Capabilities.ImageSelection = samsung.SupportSupported
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snap, observation, state := showLibrary(t, 3)
			if test.mutate != nil {
				test.mutate(&state, &observation)
			}
			plan, err := BuildPlan(snap, observation, state, Policy{Show: test.policy})
			if err != nil {
				t.Fatal(err)
			}
			if due := len(selectCommands(plan.Commands)) == 1; due != test.wantDue {
				t.Fatalf("change planned = %t, want %t: %+v", due, test.wantDue, plan.Commands)
			}
		})
	}
}

func TestPlanShowSkipsWhatIsAlreadyOnScreen(t *testing.T) {
	snap, observation, state := showLibrary(t, 2)
	observation.CurrentArtwork = "id-a.jpg"
	plan, err := BuildPlan(snap, observation, state, Policy{Show: ShowPolicy{Interval: time.Minute, Order: ShowSequential}})
	if err != nil {
		t.Fatal(err)
	}
	if selects := selectCommands(plan.Commands); len(selects) != 1 || selects[0].ContentID != "id-b.jpg" {
		t.Fatalf("selects = %+v, want id-b.jpg", selects)
	}

	single, singleObservation, singleState := showLibrary(t, 1)
	singleObservation.CurrentArtwork = "id-a.jpg"
	plan, err = BuildPlan(single, singleObservation, singleState, Policy{Show: ShowPolicy{Interval: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	if selects := selectCommands(plan.Commands); len(selects) != 0 {
		t.Fatalf("re-selected the only picture already on screen: %+v", selects)
	}
}

func TestRunRecordsAVerifiedPictureChange(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	snap, observation, state := showLibrary(t, 2)
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatalf("seed reconciliation state: %v", err)
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation},
		receipts:     []samsung.Receipt{{CommandID: "select", Outcome: samsung.OutcomeApplied, CompletedAt: testTime}},
	}
	service := newTestService(t, directory, Policy{Show: ShowPolicy{Interval: 15 * time.Minute}})
	result, err := service.Run(context.Background(), Request{CycleID: "display", TV: adapter, Snapshot: snap})
	if err != nil || result.Status != StatusComplete {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Select"}) {
		t.Fatalf("applied commands = %v, want one picture change", adapter.commands)
	}
	display := result.State.Display
	if display == nil || display.Round != 1 || display.At.IsZero() || display.Picked[display.Digest] != 1 {
		t.Fatalf("display state = %+v", display)
	}
	reloaded, found, err := newStateStore(directory).load(context.Background(), state.TV)
	if err != nil || !found {
		t.Fatalf("reload state: %v", err)
	}
	if reloaded.Display == nil || reloaded.Display.Digest != display.Digest {
		t.Fatalf("display state was not persisted: %+v", reloaded.Display)
	}
}

func TestDisplayStateValidation(t *testing.T) {
	valid := DisplayState{Digest: digestHex("a"), At: testTime, Round: 2, Picked: map[string]int{digestHex("a"): 2, digestHex("b"): 1}}
	tests := []struct {
		name    string
		mutate  func(*DisplayState)
		wantErr bool
	}{
		{name: "valid"},
		{name: "bad digest", mutate: func(display *DisplayState) { display.Digest = "nope" }, wantErr: true},
		{name: "zero time", mutate: func(display *DisplayState) { display.At = time.Time{} }, wantErr: true},
		{name: "zero round", mutate: func(display *DisplayState) { display.Round = 0 }, wantErr: true},
		{name: "future round", mutate: func(display *DisplayState) { display.Picked[digestHex("b")] = 3 }, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			display := cloneDisplay(&valid)
			if test.mutate != nil {
				test.mutate(display)
			}
			if err := validateDisplay(display); (err != nil) != test.wantErr {
				t.Fatalf("validateDisplay() error = %v, want error %t", err, test.wantErr)
			}
		})
	}
	if err := validateShowPolicy(ShowPolicy{Interval: 25 * time.Hour}); err == nil {
		t.Fatal("accepted a show interval over a day")
	}
	if err := validateShowPolicy(ShowPolicy{Interval: time.Minute, Order: ShowSequential + 1}); err == nil {
		t.Fatal("accepted an unknown show order")
	}
}
