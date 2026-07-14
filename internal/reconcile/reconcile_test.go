package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

var testTime = time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type sequenceIDs struct {
	mu sync.Mutex
	n  int
}

func (s *sequenceIDs) NewID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("operation-%d", s.n), nil
}

type fakeAdapter struct {
	mu           sync.Mutex
	observations []samsung.Observation
	observeErrs  []error
	requests     []samsung.ObserveRequest
	receipts     []samsung.Receipt
	applyErrs    []error
	observeCalls int
	applyCalls   int
	commands     []string
}

func (f *fakeAdapter) Observe(_ context.Context, request samsung.ObserveRequest) (samsung.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	index := f.observeCalls
	f.observeCalls++
	if len(f.observations) == 0 {
		return samsung.Observation{}, errors.New("no scripted observation")
	}
	if index >= len(f.observations) {
		index = len(f.observations) - 1
	}
	var err error
	if index < len(f.observeErrs) {
		err = f.observeErrs[index]
	}
	return f.observations[index], err
}

func TestRunInitialObservationRequestsPolicyCapabilities(t *testing.T) {
	t.Parallel()

	stopObservation := errors.New("stop after initial observation")
	tests := []struct {
		name   string
		policy Policy
		want   samsung.CapabilitySet
	}{
		{
			name:   "preserve settings and power",
			policy: Policy{},
			want:   samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory,
		},
		{
			name:   "disable slideshow",
			policy: Policy{Slideshow: SlideshowPolicy{Mode: PolicyDisable}},
			want: samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory |
				samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite,
		},
		{
			name: "set slideshow",
			policy: Policy{Slideshow: SlideshowPolicy{
				Mode: PolicySet, Setting: samsung.SlideshowSetting{Interval: 15, Kind: samsung.SlideshowSequential},
			}},
			want: samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory |
				samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite,
		},
		{
			name:   "disable brightness",
			policy: Policy{Brightness: SettingPolicy{Mode: PolicyDisable}},
			want: samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory |
				samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite,
		},
		{
			name:   "set brightness",
			policy: Policy{Brightness: SettingPolicy{Mode: PolicySet, Value: 50}},
			want: samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory |
				samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite,
		},
		{
			name:   "power off",
			policy: Policy{Power: PowerOff},
			want: samsung.CapabilityArtStateObservation | samsung.CapabilityUserArtInventory |
				samsung.CapabilityRemotePower,
		},
		{
			name:   "power on",
			policy: Policy{Power: PowerOn},
			want:   samsung.CapabilityRemotePower,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			adapter := &fakeAdapter{
				observations: []samsung.Observation{{}},
				observeErrs:  []error{stopObservation},
			}
			service := newTestService(t, filepath.Join(t.TempDir(), "state"), test.policy)
			_, err := service.Run(context.Background(), Request{
				CycleID: "initial-capabilities", TV: adapter, Snapshot: snapshot(),
			})
			if !errors.Is(err, stopObservation) {
				t.Fatalf("Run() error = %v, want %v", err, stopObservation)
			}
			if len(adapter.requests) != 1 {
				t.Fatalf("Observe() requests = %d, want 1", len(adapter.requests))
			}
			if got := adapter.requests[0].Required; got != test.want {
				t.Fatalf("initial capabilities = %09b, want %09b", got, test.want)
			}
		})
	}
}

func (f *fakeAdapter) Apply(_ context.Context, _ samsung.Authorization, command samsung.Command) (samsung.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.applyCalls
	f.applyCalls++
	f.commands = append(f.commands, fmt.Sprintf("%T", command))
	if index >= len(f.receipts) {
		return samsung.Receipt{Outcome: samsung.OutcomeNotAttempted}, errors.New("no scripted receipt")
	}
	var err error
	if index < len(f.applyErrs) {
		err = f.applyErrs[index]
	}
	return f.receipts[index], err
}

func (f *fakeAdapter) Close(context.Context) error { return nil }
func knownObservation(power samsung.PowerState, ids ...string) samsung.Observation {
	slices.Sort(ids)
	canonical, _ := json.Marshal(ids)
	disposition := samsung.DispositionEligible
	artMode := samsung.ArtModeOn
	if power == samsung.PowerStateOff {
		disposition = samsung.DispositionBlockedPowerOff
		artMode = samsung.ArtModeUnknown
	}
	return samsung.Observation{
		TV:         samsung.TVIdentity{Address: "192.0.2.10", Model: "QN55LS03", FirmwareVersion: "1", Known: true},
		Connection: samsung.ConnectionReady, Power: power, ArtMode: artMode,
		Inventory: samsung.Inventory{
			CategoryID: "MY-C0002", ContentIDs: slices.Clone(ids), Fingerprint: sha256.Sum256(canonical),
			Known: true, ObservedAt: testTime,
		},
		Capabilities: samsung.Capabilities{
			ArtStateObservation: samsung.SupportSupported, UserArtInventory: samsung.SupportSupported,
			RemotePower: samsung.SupportSupported,
		},
		ObservedAt: testTime, Disposition: disposition,
	}
}

func snapshot(items ...collection.Item) collection.Snapshot {
	slices.SortFunc(items, func(left, right collection.Item) int {
		if left.Name < right.Name {
			return -1
		}
		if left.Name > right.Name {
			return 1
		}
		return 0
	})
	return collection.Snapshot{Generation: collectionGeneration(items), Items: items}
}

func artworkItem(name, body string) collection.Item {
	digest := sha256.Sum256([]byte(body))
	return collection.Item{
		Name: name, Path: filepath.Join("/art", name), Digest: digest, Type: collection.FileTypeJPEG,
		Size: int64(len(body)), Width: 3840, Height: 2160, Origin: collection.Origin{Key: "test", Class: collection.OriginOperator},
	}
}

func newTestService(t *testing.T, directory string, policy Policy) Service {
	t.Helper()
	value, err := New(Config{StateDirectory: directory, Policy: policy}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBuildPlanDeterministicAndDigestBased(t *testing.T) {
	first := artworkItem("same.jpg", "new bytes")
	snap := snapshot(first)
	observation := knownObservation(samsung.PowerStateOn, "old-content")
	identity, _ := identityFromObservation(observation)
	oldDigest := hex.EncodeToString(sha256.New().Sum(nil))
	state := initialState(identity)
	state.Bindings[oldDigest] = Binding{
		Digest: oldDigest, ContentID: "old-content", Name: "same.jpg",
		CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
	}

	plan, err := BuildPlan(snap, observation, state, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Commands) != 1 || plan.Commands[0].Kind != CommandUpload ||
		plan.Commands[0].Digest != hex.EncodeToString(first.Digest[:]) {
		t.Fatalf("commands = %#v", plan.Commands)
	}
	// The only owned TV image is retained until a successor is bound.
	for _, command := range plan.Commands {
		if command.Kind == CommandDeleteOwned {
			t.Fatal("planner deleted the last-known-good image")
		}
	}
	second, err := BuildPlan(snap, observation, cloneState(state), Policy{})
	if err != nil || !slices.Equal(plan.Commands, second.Commands) {
		t.Fatalf("planner is not deterministic: %#v / %#v / %v", plan, second, err)
	}
}

func TestBuildPlanPrunesAndOrdersCommands(t *testing.T) {
	item := artworkItem("wanted.jpg", "wanted")
	snap := snapshot(item)
	observation := knownObservation(samsung.PowerStateOn, "unknown")
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	missingDigest := digestHex("missing")
	state.Bindings[missingDigest] = Binding{
		Digest: missingDigest, ContentID: "gone", Name: "gone.jpg",
		CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
	}
	plan, err := BuildPlan(snap, observation, state, Policy{RemoveUnknown: true, Power: PowerOff})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.PruneBindings, []string{missingDigest}) {
		t.Fatalf("prune = %v", plan.PruneBindings)
	}
	want := []CommandKind{CommandUpload, CommandDeleteUnknown, CommandPowerOff}
	got := make([]CommandKind, len(plan.Commands))
	for index := range plan.Commands {
		got[index] = plan.Commands[index].Kind
	}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestRunDryRunHasZeroWritesAndMutation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	adapter := &fakeAdapter{observations: []samsung.Observation{knownObservation(samsung.PowerStateOn)}}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{
		CycleID: "cycle", TV: adapter, Snapshot: snapshot(artworkItem("new.jpg", "new")), DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusIncompleteDryRun || adapter.applyCalls != 0 {
		t.Fatalf("result = %#v, apply calls = %d", result, adapter.applyCalls)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run created state directory: %v", err)
	}
}

func TestRunUploadFoldsVerifiedReceipt(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	on := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{on, on},
		receipts: []samsung.Receipt{{
			CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "new-content", CompletedAt: testTime,
		}},
	}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{
		CycleID: "cycle", TV: adapter, Snapshot: snapshot(artworkItem("new.jpg", "new")),
	})
	if err != nil || result.Status != StatusComplete || adapter.applyCalls != 1 || len(result.State.Bindings) != 1 {
		t.Fatalf("result = %#v, error = %v, apply calls = %d", result, err, adapter.applyCalls)
	}
	if _, err := os.Lstat(directory); err != nil {
		t.Fatalf("upload did not persist reconciliation state: %v", err)
	}
}

func TestRunPowerOffPersistsIntentReceiptAndCompletion(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	on := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{on, on},
		receipts:     []samsung.Receipt{{CommandID: "command", Outcome: samsung.OutcomeApplied, CompletedAt: testTime}},
	}
	service := newTestService(t, directory, Policy{Power: PowerOff})
	result, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusComplete || result.Applied != 1 || result.State.Pending != nil || adapter.applyCalls != 1 {
		t.Fatalf("result = %#v, apply calls = %d", result, adapter.applyCalls)
	}
	info, err := os.Stat(filepath.Join(directory, filepath.Base(newStateStore(directory).path(result.State.TV))))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v, error = %v", info, err)
	}
	dirInfo, err := os.Stat(directory)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, error = %v", dirInfo, err)
	}
}

func TestUnknownOutcomeIsObservedAndNeverRetried(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	on := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{on, on},
		receipts:     []samsung.Receipt{{CommandID: "command", Outcome: samsung.OutcomeUnknown, CompletedAt: testTime}},
		applyErrs:    []error{errors.New("lost acknowledgement")},
	}
	service := newTestService(t, directory, Policy{Power: PowerOff})
	result, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()})
	if !errors.Is(err, ErrRecoveryRequired) || result.State.Pending == nil || result.State.Pending.Phase != PhaseOutcomeUnknown {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if adapter.applyCalls != 1 {
		t.Fatalf("apply calls = %d", adapter.applyCalls)
	}
	adapter.observations = append(adapter.observations, knownObservation(samsung.PowerStateOff))
	restarted := newTestService(t, directory, Policy{Power: PowerOff})
	recovery, err := restarted.Run(context.Background(), Request{
		CycleID: "recovery", TV: adapter, Snapshot: snapshot(),
	})
	if err != nil || recovery.State.Pending != nil {
		t.Fatalf("recovery = %#v, error = %v", recovery, err)
	}
	if adapter.applyCalls != 1 {
		t.Fatalf("recovery blindly retried mutation; calls = %d", adapter.applyCalls)
	}
}

func TestResolvePendingMatrix(t *testing.T) {
	observationPresent := knownObservation(samsung.PowerStateOn, "content")
	identity, _ := identityFromObservation(observationPresent)
	digest := digestHex("art")
	base := initialState(identity)
	base.Bindings[digest] = Binding{
		Digest: digest, ContentID: "content", Name: "art.jpg",
		CollectionGeneration: digestHex(""), ConfirmedAt: testTime,
	}
	tests := []struct {
		name        string
		pending     Pending
		observation samsung.Observation
		resolved    bool
		wantBinding bool
	}{
		{
			name: "delete absent applied", observation: knownObservation(samsung.PowerStateOn), resolved: true,
			pending: Pending{Command: CommandIntent{Kind: CommandDeleteOwned, Digest: digest, ContentID: "content"}, Phase: PhaseOutcomeUnknown},
		},
		{
			name: "delete present not applied", observation: observationPresent, resolved: true, wantBinding: true,
			pending: Pending{Command: CommandIntent{Kind: CommandDeleteOwned, Digest: digest, ContentID: "content"}, Phase: PhaseOutcomeUnknown},
		},
		{
			name: "upload positive correlated", observation: observationPresent, resolved: true, wantBinding: true,
			pending: Pending{
				Command: CommandIntent{Kind: CommandUpload, Digest: digest, Name: "art.jpg"}, Phase: PhaseApplied,
				CollectionGen: base.Bindings[digest].CollectionGeneration,
				Receipt:       &ReceiptSummary{Outcome: samsung.OutcomeApplied, ContentID: "content"},
			},
		},
		{
			name: "upload uncorrelated blocked", observation: observationPresent, resolved: false, wantBinding: true,
			pending: Pending{Command: CommandIntent{Kind: CommandUpload, Digest: digest, Name: "art.jpg"}, Phase: PhaseOutcomeUnknown},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneState(base)
			state.Pending = &test.pending
			got, resolved, err := resolvePending(state, test.observation, testTime)
			if resolved != test.resolved || (err == nil) != test.resolved {
				t.Fatalf("resolved = %v, error = %v", resolved, err)
			}
			_, bound := got.Bindings[digest]
			if bound != test.wantBinding {
				t.Fatalf("binding present = %v", bound)
			}
		})
	}
}

func digestHex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestStateStoreRejectsCorruptionModesAndUnknownPersistence(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := TVIdentity{Address: "192.0.2.10", Model: "model"}
	state := initialState(identity)
	store := newStateStore(directory)
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := store.load(context.Background(), identity)
	if err != nil || !exists || loaded.Revision != state.Revision {
		t.Fatalf("loaded = %#v, exists = %v, error = %v", loaded, exists, err)
	}
	path := store.path(identity)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.load(context.Background(), identity); err == nil {
		t.Fatal("load accepted insecure mode")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	data[len(data)/2] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.load(context.Background(), identity); err == nil {
		t.Fatal("load accepted corrupted state")
	}

	unknownDirectory := filepath.Join(t.TempDir(), "unknown")
	unknown := newStateStore(unknownDirectory)
	unknown.replace = func(ctx context.Context, path string, mode fs.FileMode, write func(io.Writer) error) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if err := write(file); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return durablefs.ErrOutcomeUnknown
	}
	if err := unknown.save(context.Background(), state); !errors.Is(err, ErrPersistenceUnknown) {
		t.Fatalf("unknown save error = %v", err)
	}
}

func TestStateStoreRejectsOversizedStateBeforeAllocation(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := TVIdentity{Address: "192.0.2.10", Model: "model"}
	store := newStateStore(directory)
	path := store.path(identity)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxReconciliationStateBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.load(context.Background(), identity); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("load() error = %v, want bounded state rejection", err)
	}
}

func TestValidationFailsClosed(t *testing.T) {
	item := artworkItem("art.jpg", "art")
	snap := snapshot(item)
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	tests := []struct {
		name string
		fn   func() error
	}{
		{"unknown identity", func() error { _, err := identityFromObservation(samsung.Observation{}); return err }},
		{"invalid snapshot generation", func() error { broken := snap; broken.Generation = "bad"; return validateSnapshot(broken) }},
		{"duplicate inventory", func() error {
			broken := observation
			broken.Inventory.ContentIDs = []string{"same", "same"}
			return validateObservation(broken)
		}},
		{"state version", func() error { broken := state; broken.Version = 99; return validateState(broken, identity) }},
		{"state identity", func() error { return validateState(state, TVIdentity{Address: "other", Model: "model"}) }},
		{"nil maps", func() error { broken := state; broken.Bindings = nil; return validateState(broken, identity) }},
		{"invalid policy", func() error { return validatePolicy(Policy{Brightness: SettingPolicy{Mode: PolicySet, Value: 101}}) }},
		{"unsupported setting", func() error {
			_, err := BuildPlan(snap, observation, state, Policy{Slideshow: SlideshowPolicy{
				Mode: PolicySet, Setting: samsung.SlideshowSetting{Interval: 1, Kind: samsung.SlideshowSequential},
			}})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.fn(); err == nil {
				t.Fatal("validation accepted unsafe input")
			}
		})
	}
}

func TestServiceSerializesConcurrentRuns(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	observation := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{observations: []samsung.Observation{observation}}
	service := newTestService(t, directory, Policy{})
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = service.Run(context.Background(), Request{
				CycleID: "cycle", TV: adapter, Snapshot: snapshot(), DryRun: true,
			})
		}()
	}
	wait.Wait()
	if adapter.observeCalls != 8 || adapter.applyCalls != 0 {
		t.Fatalf("observe = %d, apply = %d", adapter.observeCalls, adapter.applyCalls)
	}
}

func TestIntentValidationAndCapabilityMapping(t *testing.T) {
	previous, desired := 1, 2
	previousSlideshow := samsung.SlideshowSetting{Interval: 1, Kind: samsung.SlideshowShuffle}
	desiredSlideshow := samsung.SlideshowSetting{Interval: 2, Kind: samsung.SlideshowSequential}
	digest := digestHex("art")
	valid := []CommandIntent{
		{Kind: CommandUpload, Digest: digest, Name: "art.jpg", Path: "/art/art.jpg", FileType: collection.FileTypeJPEG, Size: 3, Matte: defaultMatte},
		{Kind: CommandDeleteOwned, Digest: digest, ContentID: "content"},
		{Kind: CommandDeleteUnknown, ContentID: "content", RemoveUnknownApproved: true},
		{Kind: CommandSelect, ContentID: "content"},
		{Kind: CommandSlideshow, PreviousSlideshow: &previousSlideshow, DesiredSlideshow: &desiredSlideshow},
		{Kind: CommandBrightness, PreviousValue: &previous, DesiredValue: &desired},
		{Kind: CommandPowerOff},
		{Kind: CommandWake},
	}
	for _, intent := range valid {
		if err := validateIntent(intent); err != nil {
			t.Errorf("validate %s: %v", intent.Kind, err)
		}
	}
	invalid := []CommandIntent{
		{Kind: CommandUpload},
		{Kind: CommandDeleteOwned},
		{Kind: CommandDeleteUnknown, ContentID: "content"},
		{Kind: CommandSelect},
		{Kind: CommandSlideshow},
		{Kind: CommandBrightness},
		{Kind: "future"},
	}
	for _, intent := range invalid {
		if err := validateIntent(intent); err == nil {
			t.Errorf("invalid %s accepted", intent.Kind)
		}
	}
	want := baseCapabilities() |
		samsung.CapabilityImageUpload | samsung.CapabilityImageDeletion | samsung.CapabilityImageSelection |
		samsung.CapabilitySlideshowRead | samsung.CapabilitySlideshowWrite |
		samsung.CapabilityBrightnessRead | samsung.CapabilityBrightnessWrite | samsung.CapabilityRemotePower
	if got := requiredCapabilities(valid); got != want {
		t.Fatalf("capabilities = %b, want %b", got, want)
	}
	for _, intent := range valid[len(valid)-2:] {
		if _, err := samsungCommand(intent); err != nil {
			t.Errorf("map %s: %v", intent.Kind, err)
		}
	}
	if command, err := samsungCommand(valid[0]); err != nil {
		t.Fatalf("upload mapping error = %v", err)
	} else if _, ok := command.(samsung.Upload); !ok {
		t.Fatalf("upload mapping type = %T", command)
	}
	if _, err := samsungCommand(CommandIntent{Kind: "future"}); !errors.Is(err, ErrUnsupportedIntent) {
		t.Fatalf("unknown mapping error = %v", err)
	}
}

func TestValidationStateBranches(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	digest := digestHex("art")
	generation := digestHex("generation")
	binding := Binding{
		Digest: digest, ContentID: "content", Name: "art.jpg",
		CollectionGeneration: generation, ConfirmedAt: testTime,
	}
	validPending := Pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: generation,
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: sha256.Sum256([]byte("inventory"))},
		Command:           CommandIntent{Kind: CommandPowerOff}, Phase: PhasePrepared,
	}
	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{"duplicate content", func(state *State) {
			state.Bindings[digestHex("other")] = Binding{Digest: digestHex("other"), ContentID: "content", Name: "other.jpg", CollectionGeneration: generation, ConfirmedAt: testTime}
		}},
		{"binding key", func(state *State) { state.Bindings["bad"] = binding }},
		{"blank content", func(state *State) { changed := binding; changed.ContentID = " "; state.Bindings[digest] = changed }},
		{"bad name", func(state *State) { changed := binding; changed.Name = "../art.jpg"; state.Bindings[digest] = changed }},
		{"bad evidence", func(state *State) {
			changed := binding
			changed.ConfirmedAt = time.Time{}
			state.Bindings[digest] = changed
		}},
		{"bad tombstone key", func(state *State) { state.Tombstones["wrong"] = Tombstone{ContentID: "content", RecordedAt: testTime} }},
		{"bad tombstone digest", func(state *State) {
			state.Tombstones["content"] = Tombstone{ContentID: "content", Digest: "bad", RecordedAt: testTime}
		}},
		{"bad capacity", func(state *State) { state.Capacity = CapacityEvidence{Known: true, Maximum: -1} }},
		{"bad last generation", func(state *State) { state.LastCollectionGen = "bad" }},
		{"pending IDs", func(state *State) { pending := validPending; pending.OperationID = ""; state.Pending = &pending }},
		{"pending fingerprint", func(state *State) {
			pending := validPending
			pending.PolicyFingerprint = [sha256.Size]byte{}
			state.Pending = &pending
		}},
		{"pending phase", func(state *State) { pending := validPending; pending.Phase = 99; state.Pending = &pending }},
		{"pending command", func(state *State) { pending := validPending; pending.Command.Kind = "bad"; state.Pending = &pending }},
		{"applied no receipt", func(state *State) { pending := validPending; pending.Phase = PhaseApplied; state.Pending = &pending }},
		{"prepared receipt", func(state *State) {
			pending := validPending
			pending.Receipt = &ReceiptSummary{Outcome: samsung.OutcomeApplied}
			state.Pending = &pending
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := initialState(identity)
			state.Bindings[digest] = binding
			test.mutate(&state)
			if err := validateState(state, identity); err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
	applied := validPending
	applied.Phase = PhaseApplied
	applied.Receipt = &ReceiptSummary{CommandID: "command", Outcome: samsung.OutcomeApplied, CompletedAt: testTime}
	state := initialState(identity)
	state.Pending = &applied
	if err := validateState(state, identity); err != nil {
		t.Fatalf("valid applied pending rejected: %v", err)
	}
}

func TestResolvePendingRemainingCommands(t *testing.T) {
	on := knownObservation(samsung.PowerStateOn, "content")
	off := knownObservation(samsung.PowerStateOff)
	identity, _ := identityFromObservation(on)
	base := initialState(identity)
	tests := []struct {
		name        string
		command     CommandIntent
		phase       Phase
		observation samsung.Observation
		resolved    bool
	}{
		{"power prepared on", CommandIntent{Kind: CommandPowerOff}, PhasePrepared, on, true},
		{"power unknown off", CommandIntent{Kind: CommandPowerOff}, PhaseOutcomeUnknown, off, true},
		{"wake prepared off", CommandIntent{Kind: CommandWake}, PhasePrepared, off, true},
		{"wake unknown on", CommandIntent{Kind: CommandWake}, PhaseOutcomeUnknown, on, true},
		{"select blocked", CommandIntent{Kind: CommandSelect, ContentID: "content"}, PhaseOutcomeUnknown, on, false},
		{"slideshow blocked", CommandIntent{Kind: CommandSlideshow}, PhaseOutcomeUnknown, on, false},
		{"brightness blocked", CommandIntent{Kind: CommandBrightness}, PhaseOutcomeUnknown, on, false},
		{"invalid blocked", CommandIntent{Kind: "invalid"}, PhaseOutcomeUnknown, on, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneState(base)
			state.Pending = &Pending{Command: test.command, Phase: test.phase}
			got, resolved, err := resolvePending(state, test.observation, testTime)
			if resolved != test.resolved || (err == nil) != test.resolved {
				t.Fatalf("state = %#v, resolved = %v, error = %v", got, resolved, err)
			}
		})
	}
}

func TestHandleReceiptNotAppliedInvalidAndPersistence(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	state.Pending = &Pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: observation.Inventory.Fingerprint},
		Command:           CommandIntent{Kind: CommandPowerOff}, Phase: PhasePrepared,
	}
	concrete := newTestService(t, directory, Policy{}).(*service)
	for _, outcome := range []samsung.Outcome{samsung.OutcomeNotAttempted, samsung.OutcomeNotApplied} {
		copyState := cloneState(state)
		got, _, err := concrete.handleReceipt(context.Background(), copyState, observation, samsung.Receipt{Outcome: outcome}, nil)
		if err == nil || got.Pending != nil {
			t.Fatalf("outcome %d: state = %#v, error = %v", outcome, got, err)
		}
	}
	if _, _, err := concrete.handleReceipt(context.Background(), state, observation, samsung.Receipt{Outcome: 99}, nil); err == nil {
		t.Fatal("invalid outcome accepted")
	}
}

func TestRunAbsentBlockedAndDryRunProjection(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	service := newTestService(t, directory, Policy{})
	adapter := &fakeAdapter{observations: []samsung.Observation{knownObservation(samsung.PowerStateOn)}}
	recovery, err := service.Run(context.Background(), Request{
		CycleID: "recovery", TV: adapter, Snapshot: snapshot(), DryRun: true,
	})
	if err != nil || recovery.Status != StatusIncompleteDryRun {
		t.Fatalf("recovery = %#v, error = %v", recovery, err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recovery dry run wrote state: %v", err)
	}

	blocked := knownObservation(samsung.PowerStateOff)
	adapter = &fakeAdapter{observations: []samsung.Observation{blocked}}
	result, err := service.Run(context.Background(), Request{CycleID: "skip", TV: adapter, Snapshot: snapshot()})
	if err != nil || result.Status != StatusKnownSkip {
		t.Fatalf("skip result = %#v, error = %v", result, err)
	}
}

func TestConstructionHelpersAndErrorStatus(t *testing.T) {
	if _, err := New(Config{StateDirectory: "relative"}, Dependencies{}); err == nil {
		t.Fatal("relative directory accepted")
	}
	if _, err := New(Config{StateDirectory: filepath.Join(t.TempDir(), "state"), Policy: Policy{Power: 99}}, Dependencies{}); err == nil {
		t.Fatal("invalid default policy accepted")
	}
	service, err := New(Config{StateDirectory: filepath.Join(t.TempDir(), "state")}, Dependencies{})
	if err != nil || service == nil {
		t.Fatalf("default construction: %v", err)
	}
	if _, err := (randomIDs{}).NewID(); err != nil {
		t.Fatal(err)
	}
	if (wallClock{}).Now().IsZero() {
		t.Fatal("wall clock returned zero")
	}
	checks := []struct {
		err  error
		want Status
	}{
		{ErrPersistenceUnknown, StatusPersistenceUnknown},
		{ErrRecoveryRequired, StatusRecoveryRequired},
		{ErrUnsupportedIntent, StatusUnsupported},
		{errors.New("ordinary"), StatusNotApplied},
	}
	for _, check := range checks {
		if got := statusForError(check.err); got != check.want {
			t.Errorf("status = %d, want %d", got, check.want)
		}
	}
	if dryRunStatus(true) != StatusIncompleteDryRun || dryRunStatus(false) != StatusComplete {
		t.Fatal("dry-run status mapping is invalid")
	}
}

func TestRunNoopCompletionReloadAndApplyRejection(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	on := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{observations: []samsung.Observation{on}}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{CycleID: "first", TV: adapter, Snapshot: snapshot()})
	if err != nil || result.Status != StatusComplete || result.State.LastCompleteCycle != "first" {
		t.Fatalf("first = %#v, error = %v", result, err)
	}
	result, err = service.Run(context.Background(), Request{CycleID: "second", TV: adapter, Snapshot: snapshot()})
	if err != nil || result.State.Revision <= 2 || result.State.LastCompleteCycle != "second" {
		t.Fatalf("second = %#v, error = %v", result, err)
	}

	rejectDirectory := filepath.Join(t.TempDir(), "reject")
	reject := &fakeAdapter{
		observations: []samsung.Observation{on, on},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{errors.New("rejected")},
	}
	service = newTestService(t, rejectDirectory, Policy{Power: PowerOff})
	result, err = service.Run(context.Background(), Request{CycleID: "reject", TV: reject, Snapshot: snapshot()})
	if err == nil || result.Status != StatusNotApplied || result.State.Pending != nil || reject.applyCalls != 1 {
		t.Fatalf("rejected = %#v, error = %v", result, err)
	}
}

func TestRunObservationAndFreshnessFailures(t *testing.T) {
	on := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{observations: []samsung.Observation{on}, observeErrs: []error{errors.New("offline")}}
	service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{})
	if result, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()}); err == nil || result.Observation.TV.Model == "" {
		t.Fatalf("observation failure result = %#v, error = %v", result, err)
	}

	changed := knownObservation(samsung.PowerStateOn, "external")
	adapter = &fakeAdapter{observations: []samsung.Observation{on, changed}}
	service = newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{Power: PowerOff})
	if _, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()}); err == nil {
		t.Fatal("changed inventory authorized mutation")
	}
}

func TestBuildPlanDeletionAndRenameCases(t *testing.T) {
	desiredItem := artworkItem("renamed.jpg", "desired")
	snap := snapshot(desiredItem)
	observation := knownObservation(samsung.PowerStateOn, "desired-id", "obsolete-id")
	identity, _ := identityFromObservation(observation)
	desiredDigest := hex.EncodeToString(desiredItem.Digest[:])
	obsoleteDigest := digestHex("obsolete")
	state := initialState(identity)
	state.Bindings[desiredDigest] = Binding{
		Digest: desiredDigest, ContentID: "desired-id", Name: "old-name.jpg",
		CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
	}
	state.Bindings[obsoleteDigest] = Binding{
		Digest: obsoleteDigest, ContentID: "obsolete-id", Name: "obsolete.jpg",
		CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
	}
	plan, err := BuildPlan(snap, observation, state, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Commands) != 1 || plan.Commands[0].Kind != CommandDeleteOwned || plan.Commands[0].Digest != obsoleteDigest {
		t.Fatalf("commands = %#v", plan.Commands)
	}
	for _, command := range plan.Commands {
		if command.Kind == CommandUpload && command.Digest == desiredDigest {
			t.Fatal("same-digest rename caused upload")
		}
	}
}

func TestObservationSnapshotAndRequestValidationBranches(t *testing.T) {
	validObservation := knownObservation(samsung.PowerStateOn)
	observationCases := []func(*samsung.Observation){
		func(value *samsung.Observation) { value.Inventory.Known = false },
		func(value *samsung.Observation) { value.Inventory.CategoryID = "" },
		func(value *samsung.Observation) { value.Inventory.ContentIDs = []string{" blank "} },
		func(value *samsung.Observation) { value.Inventory.Fingerprint = [sha256.Size]byte{} },
	}
	for index, mutate := range observationCases {
		value := validObservation
		mutate(&value)
		if err := validateObservation(value); err == nil {
			t.Errorf("observation case %d accepted", index)
		}
	}
	item := artworkItem("art.jpg", "art")
	validSnapshot := snapshot(item)
	snapshotCases := []func(*collection.Snapshot){
		func(value *collection.Snapshot) { value.Items = append(value.Items, value.Items[0]) },
		func(value *collection.Snapshot) { value.Items[0].Path = "relative" },
		func(value *collection.Snapshot) { value.Items[0].Name = "changed.jpg" },
	}
	for index, mutate := range snapshotCases {
		value := validSnapshot
		value.Items = slices.Clone(validSnapshot.Items)
		mutate(&value)
		if err := validateSnapshot(value); err == nil {
			t.Errorf("snapshot case %d accepted", index)
		}
	}
	adapter := &fakeAdapter{}
	requests := []Request{
		{TV: adapter, Snapshot: validSnapshot},
		{CycleID: "cycle", Snapshot: validSnapshot},
		{CycleID: "cycle", TV: adapter, Snapshot: collection.Snapshot{}},
		{CycleID: "cycle", TV: adapter, Snapshot: func() collection.Snapshot { value := validSnapshot; value.DryRun = true; return value }()},
	}
	for index, request := range requests {
		if err := validateRequest(request.CycleID, request.TV, request.Snapshot, request.Policy, false); err == nil {
			t.Errorf("request case %d accepted", index)
		}
	}
}

func TestStateDecodeDirectoryAndFileSafetyBranches(t *testing.T) {
	identity := TVIdentity{Address: "192.0.2.10", Model: "model"}
	state := initialState(identity)
	data, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeState(append(slices.Clone(data), []byte(`{}`)...), identity); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	unknown := slices.Clone(data)
	unknown = slices.Replace(unknown, 1, 1, []byte(`  "unknown": true,`)...)
	if _, err := decodeState(unknown, identity); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := decodeState(data, TVIdentity{Address: "other", Model: "model"}); err == nil {
		t.Fatal("wrong identity accepted")
	}

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := newStateStore(link).validateDirectory(false); err == nil {
		t.Fatal("symlink directory accepted")
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := newStateStore(target).validateDirectory(false); err == nil {
		t.Fatal("insecure directory accepted")
	}

	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store := newStateStore(directory)
	if err := os.Symlink(filepath.Join(directory, "missing"), store.path(identity)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.load(context.Background(), identity); err == nil {
		t.Fatal("symlink state accepted")
	}
}

func TestRunExistingNoPendingAndBlockedPending(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	store := newStateStore(directory)
	state := initialState(identity)
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{observations: []samsung.Observation{observation}}
	service := newTestService(t, directory, Policy{})
	recovery, err := service.Run(context.Background(), Request{CycleID: "recover", TV: adapter, Snapshot: snapshot()})
	if err != nil || recovery.State.Pending != nil {
		t.Fatalf("no-pending recovery = %#v, error = %v", recovery, err)
	}

	state.Pending = &Pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: observation.Inventory.Fingerprint},
		Command: CommandIntent{
			Kind: CommandUpload, Digest: digestHex("art"), Name: "art.jpg", Path: "/art/art.jpg", FileType: collection.FileTypeJPEG, Size: 3, Matte: defaultMatte,
		},
		Phase: PhaseOutcomeUnknown,
	}
	state.Revision++
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter = &fakeAdapter{observations: []samsung.Observation{observation}}
	service = newTestService(t, directory, Policy{})
	recovery, err = service.Run(context.Background(), Request{CycleID: "recover", TV: adapter, Snapshot: snapshot()})
	if !errors.Is(err, ErrRecoveryRequired) || recovery.Status != StatusRecoveryRequired {
		t.Fatalf("blocked recovery = %#v, error = %v", recovery, err)
	}
}

type failingIDs struct{}

func (failingIDs) NewID() (string, error) { return "", errors.New("entropy failed") }

func TestExecuteAndPersistenceFailureBranches(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	adapter := &fakeAdapter{}
	request := Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()}
	concrete := newTestService(t, directory, Policy{}).(*service)
	concrete.ids = failingIDs{}
	if _, _, err := concrete.execute(context.Background(), executionInput{
		request: request, state: state, observation: observation, intent: CommandIntent{Kind: CommandPowerOff},
	}); err == nil {
		t.Fatal("operation ID failure was ignored")
	}

	invalid := state
	invalid.Revision = 0
	if err := concrete.store.save(context.Background(), invalid); err == nil {
		t.Fatal("invalid state was persisted")
	}
	failingStore := newStateStore(filepath.Join(t.TempDir(), "failing"))
	failingStore.replace = func(context.Context, string, fs.FileMode, func(io.Writer) error) error {
		return errors.New("disk failed")
	}
	if err := failingStore.save(context.Background(), state); err == nil || errors.Is(err, ErrPersistenceUnknown) {
		t.Fatalf("ordinary persistence failure = %v", err)
	}

	badPathDirectory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(badPathDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	badStore := newStateStore(badPathDirectory)
	if err := os.Mkdir(badStore.path(identity), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := badStore.load(context.Background(), identity); err == nil {
		t.Fatal("directory state file accepted")
	}
}

func TestPlannerPendingAllowEmptyAndPolicyHelpers(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn, "obsolete")
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	digest := digestHex("obsolete")
	state.Bindings[digest] = Binding{
		Digest: digest, ContentID: "obsolete", Name: "obsolete.jpg",
		CollectionGeneration: digestHex("generation"), ConfirmedAt: testTime,
	}
	plan, err := BuildPlan(snapshot(), observation, state, Policy{AllowEmpty: true})
	if err != nil || len(plan.Commands) != 1 || plan.Commands[0].Kind != CommandDeleteOwned {
		t.Fatalf("allow-empty plan = %#v, error = %v", plan, err)
	}
	state.Pending = &Pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: observation.Inventory.Fingerprint},
		Command:           CommandIntent{Kind: CommandPowerOff}, Phase: PhasePrepared,
	}
	if _, err := BuildPlan(snapshot(), observation, state, Policy{}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("pending plan error = %v", err)
	}
	concrete := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{Power: PowerOff}).(*service)
	explicit := Policy{RemoveUnknown: true}
	if got := concrete.effectivePolicy(explicit); got != normalizePolicy(explicit) {
		t.Fatalf("effective explicit policy = %#v", got)
	}
	if _, err := fingerprintPolicy(Policy{Brightness: SettingPolicy{Mode: 99}}); err == nil {
		t.Fatal("invalid policy fingerprint succeeded")
	}
}

func TestBuildPlanRejectsEveryUntrustedInputLayer(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	snap := snapshot()
	tests := []struct {
		name        string
		snapshot    collection.Snapshot
		observation samsung.Observation
		state       State
		policy      Policy
	}{
		{"snapshot", collection.Snapshot{}, observation, state, Policy{}},
		{"observation", snap, samsung.Observation{}, state, Policy{}},
		{"identity mismatch", snap, observation, func() State { value := state; value.TV.Model = "other"; return value }(), Policy{}},
		{"policy", snap, observation, state, Policy{Power: 99}},
		{"brightness unsupported", snap, observation, state, Policy{Brightness: SettingPolicy{Mode: PolicySet, Value: 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildPlan(test.snapshot, test.observation, test.state, test.policy); err == nil {
				t.Fatal("unsafe planner input accepted")
			}
		})
	}
	for _, identity := range []TVIdentity{
		{},
		{Address: " 192.0.2.1", Model: "model"},
		{Address: "192.0.2.1", Model: " model"},
		{Address: "192.0.2.1", Model: "model", FirmwareVersion: " 1"},
	} {
		if err := validateIdentity(identity); err == nil {
			t.Errorf("identity %#v accepted", identity)
		}
	}
}

func TestRunFailsClosedOnIdentityAndState(t *testing.T) {
	unknownIdentity := knownObservation(samsung.PowerStateOn)
	unknownIdentity.TV.Known = false
	adapter := &fakeAdapter{observations: []samsung.Observation{unknownIdentity}}
	service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{})
	if _, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()}); err == nil {
		t.Fatal("run accepted unknown identity")
	}

	observation := knownObservation(samsung.PowerStateOn)
	adapter = &fakeAdapter{observations: []samsung.Observation{observation}, observeErrs: []error{errors.New("offline")}}
	service = newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{})
	if result, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()}); err == nil {
		t.Fatalf("run observation failure = %#v, %v", result, err)
	}

	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, _ := identityFromObservation(observation)
	if err := os.WriteFile(newStateStore(directory).path(identity), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter = &fakeAdapter{observations: []samsung.Observation{observation}}
	service = newTestService(t, directory, Policy{})
	if result, err := service.Run(context.Background(), Request{CycleID: "cycle", TV: adapter, Snapshot: snapshot()}); err == nil {
		t.Fatalf("run corrupt state = %#v, %v", result, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestStateStoreFailureAndUnknownMismatchBranches(t *testing.T) {
	identity := TVIdentity{Address: "192.0.2.10", Model: "model"}
	state := initialState(identity)
	writeFailure := newStateStore(filepath.Join(t.TempDir(), "write"))
	writeFailure.replace = func(_ context.Context, _ string, _ fs.FileMode, write func(io.Writer) error) error {
		return write(failingWriter{})
	}
	if err := writeFailure.save(context.Background(), state); err == nil {
		t.Fatal("writer failure ignored")
	}

	unknownMismatch := newStateStore(filepath.Join(t.TempDir(), "unknown"))
	unknownMismatch.replace = func(context.Context, string, fs.FileMode, func(io.Writer) error) error {
		return durablefs.ErrOutcomeUnknown
	}
	if err := unknownMismatch.save(context.Background(), state); !errors.Is(err, ErrPersistenceUnknown) {
		t.Fatalf("unknown mismatch error = %v", err)
	}

	directory := filepath.Join(t.TempDir(), "missing")
	if _, exists, err := newStateStore(directory).load(context.Background(), identity); err != nil || exists {
		t.Fatalf("absent load exists = %v, error = %v", exists, err)
	}
}

func TestCloneStateDeepCopiesPendingPointers(t *testing.T) {
	previous, desired := 1, 2
	previousSlideshow := samsung.SlideshowSetting{Interval: 1, Kind: samsung.SlideshowShuffle}
	desiredSlideshow := samsung.SlideshowSetting{Interval: 2, Kind: samsung.SlideshowSequential}
	state := State{
		Bindings: map[string]Binding{"a": {}}, Tombstones: map[string]Tombstone{"b": {}},
		Pending: &Pending{
			Command: CommandIntent{
				PreviousValue: &previous, DesiredValue: &desired,
				PreviousSlideshow: &previousSlideshow, DesiredSlideshow: &desiredSlideshow,
			},
			Receipt: &ReceiptSummary{CommandID: "receipt"},
		},
	}
	clone := cloneState(state)
	*clone.Pending.Command.PreviousValue = 9
	*clone.Pending.Command.DesiredValue = 9
	clone.Pending.Command.PreviousSlideshow.Interval = 9
	clone.Pending.Command.DesiredSlideshow.Interval = 9
	clone.Pending.Receipt.CommandID = "changed"
	clone.Bindings["a"] = Binding{ContentID: "changed"}
	clone.Tombstones["b"] = Tombstone{ContentID: "changed"}
	if *state.Pending.Command.PreviousValue != 1 || *state.Pending.Command.DesiredValue != 2 ||
		state.Pending.Command.PreviousSlideshow.Interval != 1 || state.Pending.Command.DesiredSlideshow.Interval != 2 ||
		state.Pending.Receipt.CommandID != "receipt" || state.Bindings["a"].ContentID != "" || state.Tombstones["b"].ContentID != "" {
		t.Fatal("clone aliases mutable reconciliation state")
	}
}

func TestRunRecoversPreparedIntentBeforePlanning(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	state.Pending = &Pending{
		OperationID: "operation", CycleID: "old-cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: observation.Inventory.Fingerprint},
		Command:           CommandIntent{Kind: CommandPowerOff}, Phase: PhasePrepared,
	}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{observations: []samsung.Observation{observation}}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{CycleID: "new-cycle", TV: adapter, Snapshot: snapshot()})
	if err != nil || result.Status != StatusComplete || result.State.Pending != nil || result.State.LastCompleteCycle != "new-cycle" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if adapter.applyCalls != 0 {
		t.Fatalf("prepared intent was blindly replayed; calls = %d", adapter.applyCalls)
	}
}

func TestResolveRecoveryDryRunProjectsWithoutPersistence(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	state.Pending = &Pending{Command: CommandIntent{Kind: CommandPowerOff}, Phase: PhasePrepared}
	concrete := newTestService(t, directory, Policy{}).(*service)
	result, err := concrete.resolveRecovery(context.Background(), recoveryInput{
		cycleID: "cycle", dryRun: true, observation: observation, state: state,
	})
	if err != nil || !result.Resolved || result.Status != StatusIncompleteDryRun || result.State.Pending != nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry-run recovery persisted state: %v", err)
	}
}

func TestAppliedUploadReceiptFoldsBinding(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := knownObservation(samsung.PowerStateOn)
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	state.Pending = &Pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: observation.Inventory.Fingerprint},
		Command: CommandIntent{
			Kind: CommandUpload, Digest: digestHex("art"), Name: "art.jpg", Path: "/art/art.jpg", FileType: collection.FileTypeJPEG, Size: 3, Matte: defaultMatte,
		},
		Phase: PhasePrepared,
	}
	concrete := newTestService(t, directory, Policy{}).(*service)
	got, _, err := concrete.handleReceipt(context.Background(), state, observation, samsung.Receipt{
		CommandID: "command", Outcome: samsung.OutcomeApplied, ContentID: "new-content", CompletedAt: testTime,
	}, nil)
	if err != nil || got.Pending != nil || got.Bindings[digestHex("art")].ContentID != "new-content" {
		t.Fatalf("state = %#v, error = %v", got, err)
	}
}

func TestPlannerRandomizedDeterminismProperty(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	for iteration := range 250 {
		itemCount := random.Intn(7)
		items := make([]collection.Item, 0, itemCount)
		ids := make([]string, 0, itemCount+2)
		for index := range itemCount {
			items = append(items, artworkItem(fmt.Sprintf("%03d.jpg", index), fmt.Sprintf("art-%d-%d", iteration, index)))
			if random.Intn(2) == 0 {
				ids = append(ids, fmt.Sprintf("content-%03d", index))
			}
		}
		if random.Intn(2) == 0 {
			ids = append(ids, "unknown")
		}
		slices.Sort(ids)
		observation := knownObservation(samsung.PowerStateOn, ids...)
		identity, _ := identityFromObservation(observation)
		snap := snapshot(items...)
		state := initialState(identity)
		for index, item := range items {
			contentID := fmt.Sprintf("content-%03d", index)
			if !slices.Contains(ids, contentID) || random.Intn(2) == 0 {
				continue
			}
			digest := hex.EncodeToString(item.Digest[:])
			state.Bindings[digest] = Binding{
				Digest: digest, ContentID: contentID, Name: item.Name,
				CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
			}
		}
		policy := Policy{RemoveUnknown: random.Intn(2) == 0, AllowEmpty: random.Intn(2) == 0}
		first, err := BuildPlan(snap, observation, state, policy)
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		second, err := BuildPlan(snap, observation, cloneState(state), policy)
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatalf("iteration %d is nondeterministic: %#v / %#v / %v", iteration, first, second, err)
		}
		if state.Pending != nil {
			t.Fatalf("iteration %d: pure planning mutated pending state", iteration)
		}
	}
}

func TestRunMultiCommandUsesFreshAuthorizationAndConverges(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	settings := func(ids []string, slideshow samsung.SlideshowSetting, brightness int) samsung.Observation {
		value := knownObservation(samsung.PowerStateOn, ids...)
		value.Slideshow = samsung.SlideshowObservation{Setting: slideshow, Known: true, ObservedAt: testTime}
		value.Brightness = samsung.SettingObservation{Value: brightness, Known: true, ObservedAt: testTime}
		return value
	}
	shuffleFive := samsung.SlideshowSetting{Interval: 5, Kind: samsung.SlideshowShuffle}
	sequentialTen := samsung.SlideshowSetting{Interval: 10, Kind: samsung.SlideshowSequential}
	empty := settings(nil, shuffleFive, 20)
	withContent := settings([]string{"uploaded"}, shuffleFive, 20)
	withSlideshow := settings([]string{"uploaded"}, sequentialTen, 20)
	configured := settings([]string{"uploaded"}, sequentialTen, 50)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{empty, empty, withContent, withContent, withSlideshow, configured},
		receipts: []samsung.Receipt{
			{CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "uploaded", CompletedAt: testTime},
			{CommandID: "select", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "slideshow", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "brightness", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "power", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
		},
	}
	policy := Policy{
		Select: true, Power: PowerOff,
		Slideshow:  SlideshowPolicy{Mode: PolicySet, Setting: sequentialTen},
		Brightness: SettingPolicy{Mode: PolicySet, Value: 50},
	}
	service := newTestService(t, directory, policy)
	result, err := service.Run(context.Background(), Request{
		CycleID: "multi", TV: adapter, Snapshot: snapshot(artworkItem("art.jpg", "art")),
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 5 || adapter.observeCalls != 6 {
		t.Fatalf("result = %#v, error = %v, observe = %d", result, err, adapter.observeCalls)
	}
	wantCommands := []string{
		"samsung.Upload", "samsung.Select", "samsung.ConfigureSlideshow", "samsung.ConfigureBrightness", "samsung.PowerOff",
	}
	if !slices.Equal(adapter.commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", adapter.commands, wantCommands)
	}
	wantApplied := []CommandKind{CommandUpload, CommandSelect, CommandSlideshow, CommandBrightness, CommandPowerOff}
	if !slices.Equal(result.AppliedCommands, wantApplied) {
		t.Fatalf("result applied commands = %v, want %v", result.AppliedCommands, wantApplied)
	}
	if result.State.Pending != nil || len(result.State.Bindings) != 1 {
		t.Fatalf("final state = %#v", result.State)
	}
}

func TestFirmwareChangeRetainsBindingsAndRefreshesIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	oldObservation := knownObservation(samsung.PowerStateOn, "content")
	identity, _ := identityFromObservation(oldObservation)
	digest := digestHex("art")
	state := initialState(identity)
	state.Bindings[digest] = Binding{
		Digest: digest, ContentID: "content", Name: "art.jpg",
		CollectionGeneration: digestHex("generation"), ConfirmedAt: testTime,
	}
	store := newStateStore(directory)
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	newObservation := oldObservation
	newObservation.TV.FirmwareVersion = "2"
	adapter := &fakeAdapter{observations: []samsung.Observation{newObservation}}
	service := newTestService(t, directory, Policy{})
	recovery, err := service.Run(context.Background(), Request{
		CycleID: "firmware", TV: adapter, Snapshot: snapshot(),
	})
	if err != nil || recovery.State.TV.FirmwareVersion != "2" || len(recovery.State.Bindings) != 1 {
		t.Fatalf("recovery = %#v, error = %v", recovery, err)
	}
	if oldPath, newPath := store.path(identity), store.path(recovery.State.TV); oldPath != newPath {
		t.Fatalf("firmware changed stable path: %s != %s", oldPath, newPath)
	}
}

func TestAppliedReceiptResolutionMatrix(t *testing.T) {
	digest := digestHex("art")
	base := State{Bindings: map[string]Binding{digest: {Digest: digest, ContentID: "content"}}, Tombstones: map[string]Tombstone{"content": {ContentID: "content"}}}
	tests := []struct {
		name     string
		command  CommandIntent
		receipt  ReceiptSummary
		resolved bool
	}{
		{"upload", CommandIntent{Kind: CommandUpload, Digest: digest, Name: "art.jpg"}, ReceiptSummary{Outcome: samsung.OutcomeApplied, ContentID: "uploaded"}, true},
		{"upload blank receipt", CommandIntent{Kind: CommandUpload, Digest: digest, Name: "art.jpg"}, ReceiptSummary{Outcome: samsung.OutcomeApplied}, false},
		{"delete owned", CommandIntent{Kind: CommandDeleteOwned, Digest: digest, ContentID: "content"}, ReceiptSummary{Outcome: samsung.OutcomeApplied}, true},
		{"delete unknown", CommandIntent{Kind: CommandDeleteUnknown, ContentID: "content"}, ReceiptSummary{Outcome: samsung.OutcomeApplied}, true},
		{"select", CommandIntent{Kind: CommandSelect, ContentID: "content"}, ReceiptSummary{Outcome: samsung.OutcomeApplied}, true},
		{"wake", CommandIntent{Kind: CommandWake}, ReceiptSummary{Outcome: samsung.OutcomeApplied}, true},
		{"invalid", CommandIntent{Kind: "invalid"}, ReceiptSummary{Outcome: samsung.OutcomeApplied}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneState(base)
			state.Pending = &Pending{Command: test.command, Receipt: &test.receipt, Phase: PhaseApplied, CollectionGen: digestHex("generation")}
			got, resolved, err := resolveAppliedReceipt(state, testTime)
			if resolved != test.resolved || (err == nil) != test.resolved {
				t.Fatalf("state = %#v, resolved = %v, error = %v", got, resolved, err)
			}
		})
	}
}

func TestSlideshowRecoveryThreeWayRule(t *testing.T) {
	previous := samsung.SlideshowSetting{Interval: 5, Kind: samsung.SlideshowShuffle}
	desired := samsung.SlideshowSetting{Interval: 10, Kind: samsung.SlideshowSequential}
	base := State{Bindings: map[string]Binding{}, Tombstones: map[string]Tombstone{}, Pending: &Pending{
		Command: CommandIntent{Kind: CommandSlideshow, PreviousSlideshow: &previous, DesiredSlideshow: &desired},
	}}
	tests := []struct {
		name        string
		observation samsung.SlideshowObservation
		resolved    bool
	}{
		{"desired proves applied", samsung.SlideshowObservation{Setting: desired, Known: true}, true},
		{"previous proves not applied", samsung.SlideshowObservation{Setting: previous, Known: true}, true},
		{"same interval wrong kind blocks", samsung.SlideshowObservation{
			Setting: samsung.SlideshowSetting{Interval: desired.Interval, Kind: samsung.SlideshowShuffle}, Known: true,
		}, false},
		{"unknown blocks", samsung.SlideshowObservation{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, resolved, err := resolveSlideshowPending(base, cloneState(base), test.observation)
			if resolved != test.resolved || (err == nil) != test.resolved {
				t.Fatalf("state = %#v, resolved = %v, error = %v", got, resolved, err)
			}
		})
	}
	missing := cloneState(base)
	missing.Pending.Command.DesiredSlideshow = nil
	if _, resolved, err := resolveSlideshowPending(base, missing, samsung.SlideshowObservation{Known: true}); resolved || err == nil {
		t.Fatal("setting recovery accepted missing desired value")
	}
}

func TestReceiptObservationAndCommandMappingBranches(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn, "delete")
	observation = receiptObservation(observation, CommandIntent{Kind: CommandDeleteOwned, ContentID: "delete"}, samsung.Receipt{CompletedAt: testTime})
	if len(observation.Inventory.ContentIDs) != 0 {
		t.Fatalf("delete projection = %v", observation.Inventory.ContentIDs)
	}
	observation = receiptObservation(observation, CommandIntent{Kind: CommandWake}, samsung.Receipt{CompletedAt: testTime})
	if observation.Power != samsung.PowerStateOn {
		t.Fatal("wake receipt did not project power on")
	}
	if _, err := samsungCommand(CommandIntent{Kind: CommandUpload, Digest: "bad"}); err == nil {
		t.Fatal("invalid upload digest mapped to Samsung command")
	}
	previous, desired := 1, 2
	previousSlideshow := samsung.SlideshowSetting{Interval: 1, Kind: samsung.SlideshowShuffle}
	desiredSlideshow := samsung.SlideshowSetting{Interval: 2, Kind: samsung.SlideshowSequential}
	intents := []CommandIntent{
		{Kind: CommandDeleteOwned, ContentID: "content"},
		{Kind: CommandSelect, ContentID: "content"},
		{Kind: CommandSlideshow, PreviousSlideshow: &previousSlideshow, DesiredSlideshow: &desiredSlideshow},
		{Kind: CommandBrightness, PreviousValue: &previous, DesiredValue: &desired},
		{Kind: CommandWake},
	}
	for _, intent := range intents {
		if _, err := samsungCommand(intent); err != nil {
			t.Errorf("map %s: %v", intent.Kind, err)
		}
	}
}
