package samsung

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	inventory, err := a.freshCommandGuard(ctx, transport, authorization, command)
	if err != nil {
		return a.finishCommand(receipt, guardError(ctx, err))
	}
	if _, upload := command.(Upload); upload {
		prepared.upload.existing = make(map[string]struct{}, len(inventory.ContentIDs))
		for _, contentID := range inventory.ContentIDs {
			prepared.upload.existing[contentID] = struct{}{}
		}
		a.resolveUploadMattes(ctx, transport, &prepared.upload)
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
		return nil, commandError("authorize command", OutcomeNotAttempted, errNotAuthorized)
	}
	a.stateMu.Unlock()
	transport, ok := a.transport.(mutationTransport)
	if !ok {
		return nil, &Error{
			Kind: errorKindUnsupported, Operation: "authorize command", Outcome: OutcomeNotAttempted,
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

// resolveUploadMattes replaces a matte the TV does not list with "none", so a
// matte name the TV rejects cannot block the upload on every cycle. Without an
// answer to get_matte_list the requested mattes are sent unchanged.
func (a *adapter) resolveUploadMattes(ctx context.Context, transport mutationTransport, upload *preparedUpload) {
	catalog, err := transport.MatteCatalog(ctx)
	if err != nil {
		a.logger.Debug("matte list unavailable; sending requested mattes", "error", err)
		return
	}
	if upload.portraitMatte == "" {
		upload.portraitMatte = upload.matte
	}
	for _, matte := range []*string{&upload.matte, &upload.portraitMatte} {
		if !catalog.accepts(*matte) {
			a.logger.Warn("TV does not offer the configured matte; uploading without a matte", "matte", *matte)
			*matte = matteNone
		}
	}
}

func (a *adapter) freshCommandGuard(
	ctx context.Context,
	transport mutationTransport,
	authorization Authorization,
	command Command,
) (Inventory, error) {
	if err := a.guardActiveTV(ctx, transport, authorization, command); err != nil {
		return Inventory{}, err
	}
	if _, wake := command.(Wake); wake {
		return Inventory{}, nil
	}
	rawInventory, err := transport.Inventory(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("read inventory: %w", err)
	}
	inventory, err := normalizeInventory(rawInventory, a.clock.Now())
	if err != nil {
		return Inventory{}, err
	}
	if inventory.Fingerprint != authorization.inventoryFingerprint {
		return Inventory{}, errNotAuthorized
	}
	if err := guardCommandFacts(ctx, transport, inventory, command); err != nil {
		return Inventory{}, err
	}
	a.publishGuardInventory(inventory)
	return inventory, nil
}

func (a *adapter) guardActiveTV(
	ctx context.Context,
	transport mutationTransport,
	authorization Authorization,
	command Command,
) error {
	_, wake := command.(Wake)
	device, err := transport.DeviceInfo(ctx)
	if err != nil {
		if wake && authorization.presumedStandby && a.canWake() && unreachableError(err) {
			return nil
		}
		return fmt.Errorf("read device info: %w", err)
	}
	if err := validateFrameTVDevice(device); err != nil {
		return err
	}
	state, err := a.readPowerState(ctx, device, false)
	if err != nil {
		return err
	}
	if wake {
		if state.Power != PowerStateOff || !a.canWake() {
			return errNotAuthorized
		}
		return nil
	}
	if state.Power != PowerStateOn || state.ArtMode != ArtModeOn {
		return errNotAuthorized
	}
	return nil
}

func guardCommandFacts(ctx context.Context, transport mutationTransport, inventory Inventory, command Command) error {
	switch value := command.(type) {
	case Delete:
		if !slices.Contains(inventory.ContentIDs, strings.TrimSpace(value.ContentID)) {
			return errNotAuthorized
		}
	case Select:
		if !slices.Contains(inventory.ContentIDs, strings.TrimSpace(value.ContentID)) {
			return errNotAuthorized
		}
	case ConfigureSlideshow:
		current, readErr := transport.Slideshow(ctx)
		if readErr != nil {
			return fmt.Errorf("read slideshow: %w", readErr)
		}
		if current != value.Previous {
			return errNotAuthorized
		}
	case ConfigureBrightness:
		current, readErr := transport.Brightness(ctx)
		if readErr != nil {
			return fmt.Errorf("read brightness: %w", readErr)
		}
		if current.Value != value.PreviousValue || value.Value < current.Min || value.Value > current.Max {
			return errNotAuthorized
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
		return "", errNotAuthorized
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
		// TVs remove artwork asynchronously after acknowledging, or without
		// acknowledging at all, so the inventory is polled until it settles.
		return a.pollPostcondition(ctx, func(ctx context.Context) (Outcome, error) {
			return a.verifyInventoryMembership(ctx, transport, value.ContentID, false)
		})
	case Select:
		return a.pollPostcondition(ctx, func(ctx context.Context) (Outcome, error) {
			current, err := transport.CurrentArtwork(ctx)
			if err != nil {
				return OutcomeUnknown, err
			}
			if current != strings.TrimSpace(value.ContentID) {
				return OutcomeNotApplied, fmt.Errorf("TV shows %q instead of the selected artwork", current)
			}
			return OutcomeApplied, nil
		})
	case ConfigureSlideshow:
		actual, err := transport.Slideshow(ctx)
		return slideshowOutcome(actual, value.Previous, value.Desired, err)
	case ConfigureBrightness:
		actual, err := transport.Brightness(ctx)
		return settingOutcome(actual.Value, value.PreviousValue, value.Value, err)
	case Wake:
		return a.powerOutcome(ctx, PowerStateOn)
	case PowerOff:
		return a.powerOutcome(ctx, PowerStateOff)
	default:
		return OutcomeNotAttempted, errNotAuthorized
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
	Power     PowerState
	ArtMode   artModeState
	connected bool
}

// readPowerState derives the TV State from REST and, when REST reports the TV
// as on or in standby, the art channel. REST "standby" covers both network
// standby (the art channel refuses connections) and 2025+ Frames, which report
// standby while showing art, so the art channel decides; a standby TV whose
// art channel does not answer is known off. Earlier Frames report standby
// once the screen is dark, including when the motion or light sensor put Art
// Mode to sleep, so they are off without asking: selecting a picture would
// light the screen again.
func (a *adapter) readPowerState(ctx context.Context, device deviceInfo, dryRun bool) (operationalState, error) {
	rawPower := strings.TrimSpace(device.PowerState)
	power, known := parseDevicePowerState(rawPower)
	if !known {
		return operationalState{}, fmt.Errorf("unrecognized device power state %q", device.PowerState)
	}
	if rawPower == stringOff {
		return operationalState{Power: PowerStateOff}, nil
	}
	standby := rawPower == stringStandby
	if standby && !standbyMayShowArt(device) {
		return operationalState{Power: PowerStateOff}, nil
	}
	if err := a.transport.Connect(ctx, dryRun); err != nil {
		if standby && !errors.Is(err, errUnauthorized) && ctx.Err() == nil {
			return operationalState{Power: PowerStateOff}, nil
		}
		return operationalState{}, err
	}
	artMode, err := a.transport.ArtMode(ctx)
	if err != nil {
		if standby && ctx.Err() == nil {
			return operationalState{Power: PowerStateOff, connected: true}, nil
		}
		return operationalState{}, fmt.Errorf("read art mode: %w", err)
	}
	switch strings.TrimSpace(artMode) {
	case stringOn:
		return operationalState{Power: PowerStateOn, ArtMode: ArtModeOn, connected: true}, nil
	case stringOff:
		if standby {
			power = PowerStateOff
		}
		return operationalState{Power: power, ArtMode: artModeOff, connected: true}, nil
	default:
		return operationalState{}, fmt.Errorf("unrecognized art mode %q", artMode)
	}
}

func (a *adapter) powerOutcome(ctx context.Context, desired PowerState) (Outcome, error) {
	pollCtx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	lastErr := errors.New("power state has not reached the requested value")
	for {
		reached, err := a.desiredPowerObserved(pollCtx, desired)
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

// desiredPowerObserved reports whether the TV reached desired. A TV that
// stops answering REST after a power-off has gone dark: Frame TVs drop their
// network services on the way into standby, so that is evidence of success.
func (a *adapter) desiredPowerObserved(ctx context.Context, desired PowerState) (bool, error) {
	device, err := a.transport.DeviceInfo(ctx)
	if err != nil {
		return desired == PowerStateOff && unreachableError(err) && ctx.Err() == nil, err
	}
	if err := validateFrameTVDevice(device); err != nil {
		return false, err
	}
	actual, err := a.readPowerState(ctx, device, false)
	if err != nil {
		return false, err
	}
	return actual.Power == desired, nil
}

// pollPostcondition repeats check until it proves the command applied or the
// request timeout passes, returning the last outcome it observed.
func (a *adapter) pollPostcondition(
	ctx context.Context,
	check func(context.Context) (Outcome, error),
) (Outcome, error) {
	pollCtx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	for {
		outcome, err := check(pollCtx)
		if outcome == OutcomeApplied && err == nil {
			return outcome, nil
		}
		if waitErr := waitForPowerPoll(pollCtx, min(500*time.Millisecond, a.config.RequestTimeout)); waitErr != nil {
			if ctx.Err() != nil {
				return OutcomeUnknown, errors.Join(err, ctx.Err())
			}
			return outcome, err
		}
	}
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

// firstYearShowingArtInStandby is the first Frame model year that reports
// REST standby while Art Mode is lit.
const firstYearShowingArtInStandby = 25

// standbyMayShowArt reports whether a TV in REST standby may still be showing
// art. The model code starts with the two-digit model year; a TV whose year
// is unknown is asked over the art channel, as a 2025+ Frame is.
func standbyMayShowArt(device deviceInfo) bool {
	prefix, _, found := strings.Cut(strings.TrimSpace(device.Model), "_")
	if !found || len(prefix) != 2 {
		return true
	}
	year, err := strconv.Atoi(prefix)
	if err != nil {
		return true
	}
	return year >= firstYearShowingArtInStandby
}
