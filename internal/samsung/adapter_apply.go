package samsung

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Apply revalidates a single-use authorization and serializes the fresh guard,
// command write, and postcondition read as one per-TV operation.
func (a *adapter) Apply(ctx context.Context, authorization Authorization, command Command) (Receipt, error) {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	receipt := Receipt{CommandID: newRequestID(), Outcome: OutcomeNotAttempted}
	transport, err := a.authorizeCommand(ctx, authorization, command)
	if err != nil {
		return a.finishCommand(receipt, err)
	}
	defer a.invalidateAuthorization()
	prepared, cleanup, err := prepareCommand(command)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return a.finishCommand(receipt, commandError("validate command", OutcomeNotAttempted, err))
	}
	if err := a.freshCommandGuard(ctx, transport, authorization, command); err != nil {
		return a.finishCommand(receipt, commandError("refresh command guard", OutcomeNotAttempted, err))
	}
	if err := ctx.Err(); err != nil {
		return a.finishCommand(receipt, commandError("apply command", OutcomeNotAttempted, err))
	}
	contentID, err := executeMutation(ctx, transport, prepared)
	receipt.ContentID = contentID
	if err != nil {
		var classified *Error
		if errors.As(err, &classified) && classified.Outcome != OutcomeApplied {
			return a.finishCommand(receipt, classified)
		}
		return a.finishCommand(receipt, commandError(commandName(command), mutationFailureOutcome(err), err))
	}
	outcome, err := a.verifyPostcondition(ctx, transport, prepared, contentID)
	receipt.Outcome = outcome
	if err != nil {
		return a.finishCommand(receipt, commandError("verify "+commandName(command), outcome, err))
	}
	return a.finishCommand(receipt, nil)
}

func (a *adapter) authorizeCommand(
	ctx context.Context,
	authorization Authorization,
	command Command,
) (mutationTransport, error) {
	if err := ctx.Err(); err != nil {
		return nil, commandError("authorize command", OutcomeNotAttempted, err)
	}
	a.stateMu.Lock()
	required := commandCapability(command)
	valid := command != nil && authorization.adapter == a && !a.closed && !authorization.dryRun &&
		required != 0 && authorization.required&required == required &&
		authorization.connectionGeneration == a.connectionGeneration &&
		authorization.cycleID == a.runtime.CycleID &&
		authorization.collectionGeneration == a.runtime.CollectionGeneration &&
		authorization.inventoryFingerprint == a.runtime.InventoryFingerprint
	if !valid {
		a.stateMu.Unlock()
		return nil, commandError("authorize command", OutcomeNotAttempted, ErrNotAuthorized)
	}
	a.stateMu.Unlock()
	transport, ok := a.transport.(mutationTransport)
	if !ok {
		return nil, &Error{
			Kind: ErrorKindUnsupported, Operation: "authorize command", Outcome: OutcomeNotAttempted,
			Cause: errors.New("samsung mutation transport is unavailable"),
		}
	}
	return transport, nil
}

func commandCapability(command Command) CapabilitySet {
	switch command.(type) {
	case Upload:
		return CapabilityImageUpload
	case Delete:
		return CapabilityImageDeletion
	case Select:
		return CapabilityImageSelection
	case ConfigureSlideshow:
		return CapabilitySlideshowRead | CapabilitySlideshowWrite
	case ConfigureBrightness:
		return CapabilityBrightnessRead | CapabilityBrightnessWrite
	case Wake, PowerOff:
		return CapabilityRemotePower
	default:
		return 0
	}
}

func (a *adapter) freshCommandGuard(
	ctx context.Context,
	transport mutationTransport,
	authorization Authorization,
	command Command,
) error {
	if err := a.guardActiveTV(ctx, transport, command); err != nil {
		return err
	}
	if _, wake := command.(Wake); wake {
		return nil
	}
	rawInventory, err := transport.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("read inventory: %w", err)
	}
	inventory, err := normalizeInventory(rawInventory, a.clock.Now())
	if err != nil {
		return err
	}
	if inventory.Fingerprint != authorization.inventoryFingerprint {
		return ErrNotAuthorized
	}
	if err := guardCommandFacts(ctx, transport, inventory, command); err != nil {
		return err
	}
	a.publishGuardInventory(inventory)
	return nil
}

func (a *adapter) guardActiveTV(ctx context.Context, transport mutationTransport, command Command) error {
	device, err := transport.DeviceInfo(ctx)
	if err != nil {
		return fmt.Errorf("read device info: %w", err)
	}
	if err := validateFrameTVDevice(device); err != nil {
		return err
	}
	state, err := readOperationalState(ctx, transport, device)
	if err != nil {
		return err
	}
	if _, wake := command.(Wake); wake {
		if state.Power != PowerStateOff || len(a.config.MAC) != 6 {
			return ErrNotAuthorized
		}
		return nil
	}
	if state.Power != PowerStateOn || state.ArtMode != ArtModeOn {
		return ErrNotAuthorized
	}
	return nil
}

func guardCommandFacts(ctx context.Context, transport mutationTransport, inventory Inventory, command Command) error {
	switch value := command.(type) {
	case Delete:
		if !slices.Contains(inventory.ContentIDs, strings.TrimSpace(value.ContentID)) {
			return ErrNotAuthorized
		}
	case Select:
		if !slices.Contains(inventory.ContentIDs, strings.TrimSpace(value.ContentID)) {
			return ErrNotAuthorized
		}
	case ConfigureSlideshow:
		current, readErr := transport.Slideshow(ctx)
		if readErr != nil {
			return fmt.Errorf("read slideshow: %w", readErr)
		}
		if current != value.Previous {
			return ErrNotAuthorized
		}
	case ConfigureBrightness:
		current, readErr := transport.Brightness(ctx)
		if readErr != nil {
			return fmt.Errorf("read brightness: %w", readErr)
		}
		if current != value.PreviousValue {
			return ErrNotAuthorized
		}
	}
	return nil
}

func executeMutation(ctx context.Context, transport mutationTransport, command preparedCommand) (string, error) {
	switch value := command.command.(type) {
	case Upload:
		return transport.Upload(ctx, command.upload)
	case Delete:
		return "", transport.Delete(ctx, strings.TrimSpace(value.ContentID))
	case Select:
		return "", transport.Select(ctx, strings.TrimSpace(value.ContentID))
	case ConfigureSlideshow:
		return "", transport.ConfigureSlideshow(ctx, value.Desired)
	case ConfigureBrightness:
		return "", transport.ConfigureBrightness(ctx, value.Value)
	case Wake:
		return "", transport.Wake(ctx)
	case PowerOff:
		return "", transport.PowerOff(ctx)
	default:
		return "", ErrNotAuthorized
	}
}

func (a *adapter) verifyPostcondition(
	ctx context.Context,
	transport mutationTransport,
	command preparedCommand,
	contentID string,
) (Outcome, error) {
	switch value := command.command.(type) {
	case Upload:
		return a.verifyInventoryMembership(ctx, transport, contentID, true)
	case Delete:
		return a.verifyInventoryMembership(ctx, transport, value.ContentID, false)
	case Select:
		return OutcomeApplied, nil
	case ConfigureSlideshow:
		actual, err := transport.Slideshow(ctx)
		return slideshowOutcome(actual, value.Previous, value.Desired, err)
	case ConfigureBrightness:
		actual, err := transport.Brightness(ctx)
		return settingOutcome(actual, value.PreviousValue, value.Value, err)
	case Wake:
		return a.powerOutcome(ctx, transport, PowerStateOn)
	case PowerOff:
		return a.powerOutcome(ctx, transport, PowerStateOff)
	default:
		return OutcomeNotAttempted, ErrNotAuthorized
	}
}

func (a *adapter) verifyInventoryMembership(
	ctx context.Context,
	transport mutationTransport,
	contentID string,
	wantPresent bool,
) (Outcome, error) {
	raw, err := transport.Inventory(ctx)
	if err != nil {
		return OutcomeUnknown, err
	}
	inventory, err := normalizeInventory(raw, a.clock.Now())
	if err != nil {
		return OutcomeUnknown, err
	}
	a.publishGuardInventory(inventory)
	present := slices.Contains(inventory.ContentIDs, strings.TrimSpace(contentID))
	if present == wantPresent {
		return OutcomeApplied, nil
	}
	return OutcomeNotApplied, errors.New("TV inventory does not match acknowledged command")
}

// mutationFailureOutcome classifies a failed TV write: an explicit Art API
// rejection proves the command was not applied, and anything else is unknown.
func mutationFailureOutcome(err error) Outcome {
	if errors.Is(err, ErrArtAPIError) || errors.Is(err, ErrStorageFull) {
		return OutcomeNotApplied
	}
	return OutcomeUnknown
}

func settingOutcome(actual, previous, desired int, err error) (Outcome, error) {
	if err != nil {
		return OutcomeUnknown, err
	}
	if actual == desired {
		return OutcomeApplied, nil
	}
	if actual == previous {
		return OutcomeNotApplied, errors.New("setting retained its previous value")
	}
	return OutcomeUnknown, errors.New("setting changed to an unexpected value")
}

func slideshowOutcome(actual, previous, desired SlideshowSetting, err error) (Outcome, error) {
	if err != nil {
		return OutcomeUnknown, err
	}
	if actual == desired {
		return OutcomeApplied, nil
	}
	if actual == previous {
		return OutcomeNotApplied, errors.New("slideshow retained its previous setting")
	}
	return OutcomeUnknown, errors.New("slideshow changed to an unexpected setting")
}

type operationalState struct {
	Power   PowerState
	ArtMode ArtModeState
}

func readOperationalState(
	ctx context.Context,
	transport observationTransport,
	device DeviceInfo,
) (operationalState, error) {
	rawPower := strings.TrimSpace(device.PowerState)
	power, known := parseDevicePowerState(rawPower)
	if !known {
		return operationalState{}, fmt.Errorf("unrecognized device power state %q", device.PowerState)
	}
	if rawPower == stringOff {
		return operationalState{Power: PowerStateOff}, nil
	}
	artMode, err := transport.ArtMode(ctx)
	if err != nil {
		return operationalState{}, fmt.Errorf("read art mode: %w", err)
	}
	switch strings.TrimSpace(artMode) {
	case stringOn:
		return operationalState{Power: PowerStateOn, ArtMode: ArtModeOn}, nil
	case stringOff:
		if rawPower == stringStandby {
			power = PowerStateOff
		}
		return operationalState{Power: power, ArtMode: ArtModeOff}, nil
	default:
		return operationalState{}, fmt.Errorf("unrecognized art mode %q", artMode)
	}
}

func (a *adapter) powerOutcome(ctx context.Context, transport mutationTransport, desired PowerState) (Outcome, error) {
	pollCtx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	lastErr := errors.New("power state has not reached the requested value")
	for {
		reached, err := desiredPowerObserved(pollCtx, transport, desired)
		if reached {
			return OutcomeApplied, nil
		}
		if err != nil {
			lastErr = err
		}
		if err := waitForPowerPoll(pollCtx, min(100*time.Millisecond, a.config.RequestTimeout)); err != nil {
			return OutcomeUnknown, powerPollError(ctx, pollCtx, lastErr)
		}
	}
}

func desiredPowerObserved(ctx context.Context, transport mutationTransport, desired PowerState) (bool, error) {
	device, err := transport.DeviceInfo(ctx)
	if err != nil {
		return false, err
	}
	if err := validateFrameTVDevice(device); err != nil {
		return false, err
	}
	actual, err := readOperationalState(ctx, transport, device)
	if err != nil {
		return false, err
	}
	return actual.Power == desired, nil
}

func waitForPowerPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func powerPollError(callerCtx, pollCtx context.Context, lastErr error) error {
	if err := callerCtx.Err(); err != nil {
		return fmt.Errorf("poll power-state postcondition: %w", err)
	}
	return fmt.Errorf("poll power-state postcondition: %w", errors.Join(pollCtx.Err(), lastErr))
}
