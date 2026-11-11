package samsung

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type scriptedMutationTransport struct {
	*scriptedObservationTransport
	devices             []deviceInfo
	inventories         []json.RawMessage
	slideshowValues     []SlideshowSetting
	slideshowWrites     []SlideshowSetting
	brightnessValues    []int
	uploadContentID     string
	uploadErr           error
	deleteErr           error
	selectErr           error
	slideshowWriteErr   error
	brightnessWriteErr  error
	wakeErr             error
	powerOffErr         error
	uploadCalls         int
	deleteCalls         int
	selectCalls         int
	slideshowWriteCalls int
	brightnessCalls     int
	wakeCalls           int
	powerOffCalls       int
	deviceInfoCalls     int
	current             string
	currentErr          error
	uploadMattes        [2]string
	matteErr            error
	wakeTurnsOn         bool
	pinnedCurrent       string
}

func TestSettingOutcomeClassifiesPostconditionEvidence(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("read setting")
	tests := []struct {
		name     string
		actual   int
		previous int
		desired  int
		readErr  error
		outcome  Outcome
		wantErr  bool
	}{
		{name: "read failed", readErr: wantErr, outcome: OutcomeUnknown, wantErr: true},
		{name: "desired observed", actual: 7, previous: 3, desired: 7, outcome: OutcomeApplied},
		{name: "previous retained", actual: 3, previous: 3, desired: 7, outcome: OutcomeNotApplied, wantErr: true},
		{name: "unexpected value", actual: 5, previous: 3, desired: 7, outcome: OutcomeUnknown, wantErr: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, err := settingOutcome(testCase.actual, testCase.previous, testCase.desired, testCase.readErr)
			if outcome != testCase.outcome || (err != nil) != testCase.wantErr {
				t.Fatalf("settingOutcome() = %v, %v; want %v, error=%v", outcome, err, testCase.outcome, testCase.wantErr)
			}
			if testCase.readErr != nil && !errors.Is(err, testCase.readErr) {
				t.Fatalf("settingOutcome() error = %v, want %v", err, testCase.readErr)
			}
		})
	}
}

func TestSlideshowOutcomeClassifiesPostconditionEvidence(t *testing.T) {
	t.Parallel()

	previous := SlideshowSetting{Interval: 5, Kind: SlideshowSequential}
	desired := SlideshowSetting{Interval: 10, Kind: SlideshowShuffle}
	wantErr := errors.New("read slideshow")
	tests := []struct {
		name    string
		actual  SlideshowSetting
		readErr error
		outcome Outcome
		wantErr bool
	}{
		{name: "read failed", readErr: wantErr, outcome: OutcomeUnknown, wantErr: true},
		{name: "desired observed", actual: desired, outcome: OutcomeApplied},
		{name: "previous retained", actual: previous, outcome: OutcomeNotApplied, wantErr: true},
		{name: "unexpected value", actual: SlideshowSetting{Interval: 15, Kind: SlideshowShuffle}, outcome: OutcomeUnknown, wantErr: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, err := slideshowOutcome(testCase.actual, previous, desired, testCase.readErr)
			if outcome != testCase.outcome || (err != nil) != testCase.wantErr {
				t.Fatalf("slideshowOutcome() = %v, %v; want %v, error=%v", outcome, err, testCase.outcome, testCase.wantErr)
			}
			if testCase.readErr != nil && !errors.Is(err, testCase.readErr) {
				t.Fatalf("slideshowOutcome() error = %v, want %v", err, testCase.readErr)
			}
		})
	}
}

func (t *scriptedMutationTransport) DeviceInfo(ctx context.Context) (deviceInfo, error) {
	t.deviceInfoCalls++
	if len(t.devices) == 0 {
		return t.scriptedObservationTransport.DeviceInfo(ctx)
	}
	value := t.devices[0]
	t.devices = t.devices[1:]
	return value, nil
}

func (t *scriptedMutationTransport) Inventory(ctx context.Context) (json.RawMessage, error) {
	if len(t.inventories) == 0 {
		return t.scriptedObservationTransport.Inventory(ctx)
	}
	value := t.inventories[0]
	t.inventories = t.inventories[1:]
	return value, nil
}

func (t *scriptedMutationTransport) Upload(_ context.Context, upload preparedUpload) (string, error) {
	t.uploadCalls++
	t.uploadMattes = [2]string{upload.matte, upload.portraitMatte}
	if upload.file == nil || upload.size <= 0 {
		return "", errors.New("invalid prepared upload")
	}
	return t.uploadContentID, t.uploadErr
}

func (t *scriptedMutationTransport) Delete(context.Context, string) error {
	t.deleteCalls++
	return t.deleteErr
}

func (t *scriptedMutationTransport) Select(_ context.Context, contentID string) error {
	t.selectCalls++
	if t.selectErr == nil {
		t.current = contentID
	}
	if t.pinnedCurrent != "" {
		t.current = t.pinnedCurrent
	}
	return t.selectErr
}

func (t *scriptedMutationTransport) Slideshow(context.Context) (SlideshowSetting, error) {
	if len(t.slideshowValues) == 0 {
		return SlideshowSetting{}, errors.New("slideshow script exhausted")
	}
	value := t.slideshowValues[0]
	t.slideshowValues = t.slideshowValues[1:]
	return value, nil
}

func (t *scriptedMutationTransport) ConfigureSlideshow(_ context.Context, setting SlideshowSetting) error {
	t.slideshowWriteCalls++
	t.slideshowWrites = append(t.slideshowWrites, setting)
	return t.slideshowWriteErr
}

func (t *scriptedMutationTransport) Brightness(context.Context) (brightnessReading, error) {
	if len(t.brightnessValues) == 0 {
		return brightnessReading{}, errors.New("brightness script exhausted")
	}
	value := t.brightnessValues[0]
	t.brightnessValues = t.brightnessValues[1:]
	return brightnessReading{Value: value, Max: 10}, nil
}

func (t *scriptedMutationTransport) CurrentArtwork(context.Context) (string, error) {
	if t.currentErr != nil {
		return "", t.currentErr
	}
	return t.current, nil
}

func (t *scriptedMutationTransport) MatteCatalog(context.Context) (matteCatalog, error) {
	if t.matteErr != nil {
		return matteCatalog{}, t.matteErr
	}
	return parseMatteCatalog([]byte(`"[{\"matte_type\":\"shadowbox\"}]"`), []byte(`"[{\"color\":\"polar\"}]"`))
}

func (t *scriptedMutationTransport) ConfigureBrightness(context.Context, int) error {
	t.brightnessCalls++
	return t.brightnessWriteErr
}

func (t *scriptedMutationTransport) Wake(context.Context) error {
	t.wakeCalls++
	if t.wakeTurnsOn && t.wakeErr == nil {
		t.deviceErr = nil
		t.device.PowerState = stringOn
		t.artMode = stringOn
	}
	return t.wakeErr
}

func (t *scriptedMutationTransport) PowerOff(context.Context) error {
	t.powerOffCalls++
	return t.powerOffErr
}

func TestAdapterUploadIsDigestBoundAndSingleUse(t *testing.T) {
	path, command := testUploadCommand(t)
	_ = path
	transport := mutationTransportForInventory(`[]`)
	transport.inventories = []json.RawMessage{
		json.RawMessage(`[]`), json.RawMessage(`[]`),
		json.RawMessage(`[{"content_id":"new-id","category_id":"MY-C0002"}]`),
	}
	transport.uploadContentID = "new-id"
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageUpload)
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, command)
	if err != nil {
		t.Fatalf("Apply(upload) error = %v", err)
	}
	if receipt.Outcome != OutcomeApplied || receipt.ContentID != "new-id" || transport.uploadCalls != 1 {
		t.Fatalf("receipt/calls = %+v/%d", receipt, transport.uploadCalls)
	}
	if _, err := adapter.Apply(context.Background(), observation.Authorization, command); !errors.Is(err, errNotAuthorized) {
		t.Fatalf("reused authorization error = %v, want ErrNotAuthorized", err)
	}
}

func TestAdapterUploadPreservesDefiniteStorageFullOutcome(t *testing.T) {
	_, command := testUploadCommand(t)
	transport := mutationTransportForInventory(`[]`)
	transport.inventories = []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`[]`)}
	transport.uploadErr = commandError("upload", OutcomeNotApplied, fmt.Errorf("image_added: %w", ErrStorageFull))
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageUpload)

	receipt, err := adapter.Apply(context.Background(), observation.Authorization, command)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != ErrorKindStorageFull ||
		receipt.Outcome != OutcomeNotApplied || transport.uploadCalls != 1 {
		t.Fatalf("Apply(upload) = %+v, %v; calls = %d", receipt, err, transport.uploadCalls)
	}
}

func TestAdapterUploadRejectsChangedBytesBeforeTVIO(t *testing.T) {
	path, command := testUploadCommand(t)
	transport := mutationTransportForInventory(`[]`)
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageUpload)
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, command)
	if err == nil || receipt.Outcome != OutcomeNotAttempted || transport.uploadCalls != 0 {
		t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.uploadCalls)
	}
}

func TestAdapterFreshInventoryPreventsStaleDelete(t *testing.T) {
	transport := mutationTransportForInventory(`[{"content_id":"id-1","category_id":"MY-C0002"}]`)
	transport.inventories = []json.RawMessage{
		json.RawMessage(`[{"content_id":"id-1","category_id":"MY-C0002"}]`), json.RawMessage(`[]`),
	}
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageDeletion)
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Delete{ContentID: "id-1"})
	if !errors.Is(err, errNotAuthorized) || receipt.Outcome != OutcomeNotAttempted || transport.deleteCalls != 0 {
		t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.deleteCalls)
	}
}

// A TV that stops answering during the reads before a command has received
// nothing. That is a timeout the reconciler may retry, not a cancellation.
func TestAdapterGuardTimeoutIsRetryableAndSendsNothing(t *testing.T) {
	transport := mutationTransportForInventory(`[{"content_id":"id-1","category_id":"MY-C0002"}]`)
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageDeletion)
	transport.inventoryErr = fmt.Errorf("read art channel: %w", context.DeadlineExceeded)
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Delete{ContentID: "id-1"})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != ErrorKindTimeout || !typed.Retryable ||
		typed.Outcome != OutcomeNotAttempted || receipt.Outcome != OutcomeNotAttempted || transport.deleteCalls != 0 {
		t.Fatalf("receipt/error/calls = %+v/%+v/%d, want a retryable timeout with nothing sent", receipt, err, transport.deleteCalls)
	}

	// When the caller's own deadline has passed, it is the caller that gave up.
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if got := guardError(expired, context.DeadlineExceeded); got.Kind != ErrorKindCanceled || got.Retryable {
		t.Fatalf("expired caller: kind %v retryable %v, want a non-retryable cancellation", got.Kind, got.Retryable)
	}
}

func TestAdapterStandbyArtModeAllowsGuardedDelete(t *testing.T) {
	const inventory = `[{"content_id":"id-1","category_id":"MY-C0002"}]`
	transport := mutationTransportForInventory(inventory)
	transport.device.PowerState = stringStandby
	transport.inventories = []json.RawMessage{
		json.RawMessage(inventory), json.RawMessage(inventory), json.RawMessage(`[]`),
	}
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageDeletion)

	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Delete{ContentID: "id-1"})
	if err != nil || receipt.Outcome != OutcomeApplied || transport.deleteCalls != 1 {
		t.Fatalf("standby Art Mode delete receipt/error/calls = %+v/%v/%d", receipt, err, transport.deleteCalls)
	}
}

func TestAdapterClassifiesMutationOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		mutationErr error
		want        Outcome
		wantKind    ErrorKind
	}{
		{name: "explicit rejection", mutationErr: ErrStorageFull, want: OutcomeNotApplied, wantKind: ErrorKindStorageFull},
		{name: "timeout after write boundary", mutationErr: context.DeadlineExceeded, want: OutcomeUnknown, wantKind: ErrorKindOutcomeUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := mutationTransportForInventory(`[{"content_id":"id-1","category_id":"MY-C0002"}]`)
			transport.inventories = []json.RawMessage{
				json.RawMessage(`[{"content_id":"id-1","category_id":"MY-C0002"}]`),
				json.RawMessage(`[{"content_id":"id-1","category_id":"MY-C0002"}]`),
			}
			transport.deleteErr = test.mutationErr
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			observation := observeForCommand(t, adapter, CapabilityImageDeletion)
			receipt, err := adapter.Apply(context.Background(), observation.Authorization, Delete{ContentID: "id-1"})
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != test.wantKind || receipt.Outcome != test.want {
				t.Fatalf("receipt/error = %+v/%v", receipt, err)
			}
		})
	}
}

func TestAdapterSettingsRequireKnownPreviousAndVerifyDesired(t *testing.T) {
	transport := mutationTransportForInventory(`[]`)
	transport.inventories = []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`[]`)}
	previous := SlideshowSetting{Interval: 10, Kind: SlideshowShuffle}
	desired := SlideshowSetting{Interval: 15, Kind: SlideshowSequential}
	transport.slideshowValues = []SlideshowSetting{previous, previous, desired}
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilitySlideshowRead|CapabilitySlideshowWrite)
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, ConfigureSlideshow{
		Previous: previous,
		Desired:  desired,
	})
	if err != nil || receipt.Outcome != OutcomeApplied || transport.slideshowWriteCalls != 1 ||
		len(transport.slideshowWrites) != 1 || transport.slideshowWrites[0] != desired {
		t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.slideshowWriteCalls)
	}
}

func TestAdapterWakeAndPowerOffRequirePostcondition(t *testing.T) {
	off := deviceInfo{ModelName: "Frame", FrameTVSupport: stringTrue, PowerState: stringOff}
	on := deviceInfo{ModelName: "Frame", FrameTVSupport: stringTrue, PowerState: stringOn}
	standby := deviceInfo{ModelName: "Frame", FrameTVSupport: stringTrue, PowerState: stringStandby}
	t.Run("wake", func(t *testing.T) {
		transport := mutationTransportForInventory(`[]`)
		transport.devices = []deviceInfo{off, off, on}
		adapter := newTestAdapterWithMAC(t, transport)
		observation := observeForCommand(t, adapter, CapabilityRemotePower)
		receipt, err := adapter.Apply(context.Background(), observation.Authorization, Wake{})
		if err != nil || receipt.Outcome != OutcomeApplied || transport.wakeCalls != 1 {
			t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.wakeCalls)
		}
	})
	t.Run("wake from standby", func(t *testing.T) {
		transport := mutationTransportForInventory(`[]`)
		transport.devices = []deviceInfo{standby, standby, on}
		transport.artMode = stringOff
		adapter := newTestAdapterWithMAC(t, transport)
		observation := observeForCommand(t, adapter, CapabilityRemotePower)
		receipt, err := adapter.Apply(context.Background(), observation.Authorization, Wake{})
		if err != nil || receipt.Outcome != OutcomeApplied || transport.wakeCalls != 1 {
			t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.wakeCalls)
		}
	})
	t.Run("power off", func(t *testing.T) {
		transport := mutationTransportForInventory(`[]`)
		transport.devices = []deviceInfo{on, on, off}
		transport.inventories = []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`[]`)}
		adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
		observation := observeForCommand(t, adapter, CapabilityRemotePower)
		receipt, err := adapter.Apply(context.Background(), observation.Authorization, PowerOff{})
		if err != nil || receipt.Outcome != OutcomeApplied || transport.powerOffCalls != 1 {
			t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.powerOffCalls)
		}
	})
	t.Run("power off reaches standby", func(t *testing.T) {
		transport := mutationTransportForInventory(`[]`)
		transport.devices = []deviceInfo{on, on, standby}
		transport.artModes = []string{stringOn, stringOn, stringOff}
		transport.inventories = []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`[]`)}
		adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
		observation := observeForCommand(t, adapter, CapabilityRemotePower)
		receipt, err := adapter.Apply(context.Background(), observation.Authorization, PowerOff{})
		if err != nil || receipt.Outcome != OutcomeApplied || transport.powerOffCalls != 1 {
			t.Fatalf("receipt/error/calls = %+v/%v/%d", receipt, err, transport.powerOffCalls)
		}
	})
}

func TestAdapterWakeRevalidatesStandbyArtMode(t *testing.T) {
	transport := mutationTransportForInventory(`[]`)
	transport.device.PowerState = stringStandby
	transport.artMode = stringOff
	adapter := newTestAdapterWithMAC(t, transport)
	observation := observeForCommand(t, adapter, CapabilityRemotePower)
	transport.artMode = stringOn

	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Wake{})
	if !errors.Is(err, errNotAuthorized) || receipt.Outcome != OutcomeNotAttempted || transport.wakeCalls != 0 {
		t.Fatalf("standby Art Mode wake receipt/error/calls = %+v/%v/%d", receipt, err, transport.wakeCalls)
	}
}

func TestAdapterPowerPostconditionPollsUntilTransition(t *testing.T) {
	t.Parallel()

	off := deviceInfo{ModelName: "Frame", FrameTVSupport: stringTrue, PowerState: stringOff}
	on := deviceInfo{ModelName: "Frame", FrameTVSupport: stringTrue, PowerState: stringOn}
	transport := mutationTransportForInventory(`[]`)
	transport.devices = []deviceInfo{off, off, off, off, on}
	adapter := newTestAdapterWithMAC(t, transport)
	adapter.config.RequestTimeout = time.Second

	observation := observeForCommand(t, adapter, CapabilityRemotePower)
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Wake{})
	if err != nil || receipt.Outcome != OutcomeApplied || transport.wakeCalls != 1 || transport.deviceInfoCalls < 5 {
		t.Fatalf("receipt/error/wake/device calls = %+v/%v/%d/%d", receipt, err, transport.wakeCalls, transport.deviceInfoCalls)
	}
}

func TestAdapterPowerPostconditionPollingIsBoundedAndCancelable(t *testing.T) {
	t.Parallel()

	off := deviceInfo{ModelName: "Frame", FrameTVSupport: stringTrue, PowerState: stringOff}
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		timeout time.Duration
	}{
		{
			name: "request timeout", timeout: 25 * time.Millisecond,
			context: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
		},
		{
			name: "caller cancellation", timeout: time.Second,
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 25*time.Millisecond)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transport := mutationTransportForInventory(`[]`)
			transport.device = off
			adapter := newTestAdapterWithMAC(t, transport)
			adapter.config.RequestTimeout = test.timeout
			observation := observeForCommand(t, adapter, CapabilityRemotePower)
			ctx, cancel := test.context()
			defer cancel()
			started := time.Now()
			receipt, err := adapter.Apply(ctx, observation.Authorization, Wake{})
			if err == nil || receipt.Outcome != OutcomeUnknown || time.Since(started) > 500*time.Millisecond {
				t.Fatalf("receipt/error/elapsed = %+v/%v/%s", receipt, err, time.Since(started))
			}
			if transport.wakeCalls != 1 || transport.deviceInfoCalls < 3 {
				t.Fatalf("wake/device calls = %d/%d", transport.wakeCalls, transport.deviceInfoCalls)
			}
		})
	}
}

func TestAdapterConcurrentApplyConsumesAuthorizationOnce(t *testing.T) {
	transport := mutationTransportForInventory(`[{"content_id":"id-1","category_id":"MY-C0002"}]`)
	transport.inventories = []json.RawMessage{
		json.RawMessage(`[{"content_id":"id-1","category_id":"MY-C0002"}]`),
		json.RawMessage(`[{"content_id":"id-1","category_id":"MY-C0002"}]`),
		json.RawMessage(`[]`),
	}
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageDeletion)
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			_, _ = adapter.Apply(context.Background(), observation.Authorization, Delete{ContentID: "id-1"})
		}()
	}
	group.Wait()
	if transport.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want one", transport.deleteCalls)
	}
}

func TestAdapterAuthorizationIsBoundToObservedCapability(t *testing.T) {
	transport := mutationTransportForInventory(`[{"content_id":"id-1","category_id":"MY-C0002"}]`)
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation := observeForCommand(t, adapter, CapabilityImageUpload)
	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Delete{ContentID: "id-1"})
	if !errors.Is(err, errNotAuthorized) || receipt.Outcome != OutcomeNotAttempted || transport.deleteCalls != 0 {
		t.Fatalf("receipt/error/delete calls = %+v/%v/%d", receipt, err, transport.deleteCalls)
	}
}

func TestDesiredPowerObservedClassifiesStandbyByArtMode(t *testing.T) {
	t.Parallel()
	unreachable := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	tests := []struct {
		name       string
		power      string
		model      string
		artMode    string
		artModeErr error
		connectErr error
		deviceErr  error
		desired    PowerState
		wantMatch  bool
		wantErr    bool
	}{
		{name: "Art Mode satisfies on", power: stringStandby, artMode: stringOn, desired: PowerStateOn, wantMatch: true},
		{name: "Art Mode does not satisfy off", power: stringStandby, artMode: stringOn, desired: PowerStateOff},
		{name: "Art Mode off satisfies off", power: stringStandby, artMode: stringOff, desired: PowerStateOff, wantMatch: true},
		{name: "Art Mode off does not satisfy on", power: stringStandby, artMode: stringOff, desired: PowerStateOn},
		{
			name: "2025 Frame showing art in standby is on", power: stringStandby, model: "25_PTM_FTV",
			artMode: stringOn, desired: PowerStateOn, wantMatch: true,
		},
		{
			name: "2024 Frame in standby is off whatever Art Mode says", power: stringStandby, model: "24_PTM_FTV_T09",
			artMode: stringOn, desired: PowerStateOff, wantMatch: true,
		},
		{
			name: "2024 Frame in standby is not on", power: stringStandby, model: "24_PTM_FTV_T09",
			artMode: stringOn, desired: PowerStateOn,
		},
		{
			name: "unparsable model year asks the art channel", power: stringStandby, model: "PTM_FTV",
			artMode: stringOn, desired: PowerStateOn, wantMatch: true,
		},
		{
			name: "standby with silent art channel is off", power: stringStandby,
			artModeErr: errors.New("art mode unavailable"), desired: PowerStateOff, wantMatch: true,
		},
		{
			name: "standby refusing the art channel is off", power: stringStandby,
			connectErr: errConnectionFailure, desired: PowerStateOff, wantMatch: true,
		},
		{
			name: "on with silent art channel is unknown", power: stringOn,
			artModeErr: errors.New("art mode unavailable"), desired: PowerStateOff, wantErr: true,
		},
		{name: "dark REST proves power off", deviceErr: unreachable, desired: PowerStateOff, wantMatch: true, wantErr: true},
		{name: "dark REST does not prove wake", deviceErr: unreachable, desired: PowerStateOn, wantErr: true},
		{name: "unknown state", power: "sleeping", desired: PowerStateOff, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := mutationTransportForInventory(`[]`)
			transport.device.PowerState = test.power
			transport.device.Model = test.model
			transport.deviceErr = test.deviceErr
			transport.connectErr = test.connectErr
			transport.artMode = test.artMode
			transport.artModeErr = test.artModeErr
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			reached, err := adapter.desiredPowerObserved(context.Background(), test.desired)
			if (err != nil) != test.wantErr || reached != test.wantMatch {
				t.Fatalf("desiredPowerObserved(%s) = %v, %v", test.name, reached, err)
			}
		})
	}
}

func mutationTransportForInventory(inventory string) *scriptedMutationTransport {
	return &scriptedMutationTransport{
		scriptedObservationTransport: eligibleObservationTransport(json.RawMessage(inventory)),
	}
}

func observeForCommand(t *testing.T, adapter *adapter, required CapabilitySet) Observation {
	t.Helper()
	observation, err := adapter.Observe(context.Background(), ObserveRequest{
		CycleID: "cycle", CollectionGeneration: "generation", Required: required,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if unauthorized(observation.Authorization) {
		t.Fatalf("Observe() withheld authorization: %+v", observation)
	}
	return observation
}

func testUploadCommand(t *testing.T) (string, Upload) {
	t.Helper()
	data := []byte("committed artwork bytes")
	path := filepath.Join(t.TempDir(), "art.jpg")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, Upload{
		Path: path, Name: filepath.Base(path), FileType: "jpg", Matte: "none",
		Digest: sha256.Sum256(data), Size: int64(len(data)),
	}
}

func newTestAdapterWithMAC(t *testing.T, transport observationTransport) *adapter {
	t.Helper()
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	adapter.config.MAC = []byte{0, 1, 2, 3, 4, 5}
	return adapter
}

// A TV lists a confirmed upload a moment after confirming it, so the
// postcondition polls; a confirmed image that never appears is parked as
// unknown, because calling it not applied would upload the artwork twice.
func TestAdapterUploadPostconditionPollsInventory(t *testing.T) {
	listed := json.RawMessage(`[{"content_id":"new-id","category_id":"MY-C0002"}]`)
	tests := []struct {
		name        string
		afterUpload []json.RawMessage
		want        Outcome
		wantErr     bool
	}{
		{name: "listed on a later read", afterUpload: []json.RawMessage{json.RawMessage(`[]`), listed}, want: OutcomeApplied},
		{name: "never listed", want: OutcomeUnknown, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, command := testUploadCommand(t)
			transport := mutationTransportForInventory(`[]`)
			transport.inventories = append([]json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`[]`)}, test.afterUpload...)
			transport.uploadContentID = "new-id"
			if test.want == OutcomeApplied {
				transport.inventory = listed
			}
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			observation := observeForCommand(t, adapter, CapabilityImageUpload)
			if test.wantErr {
				adapter.config.RequestTimeout = 50 * time.Millisecond
			}

			receipt, err := adapter.Apply(context.Background(), observation.Authorization, command)
			if (err != nil) != test.wantErr {
				t.Fatalf("Apply(upload) error = %v", err)
			}
			if receipt.Outcome != test.want || receipt.ContentID != "new-id" || transport.uploadCalls != 1 {
				t.Fatalf("receipt/calls = %+v/%d", receipt, transport.uploadCalls)
			}
		})
	}
}
