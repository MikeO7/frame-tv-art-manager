package samsung

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

const userArtCategory = "MY-C0002"

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type observationTransport interface {
	Connect(context.Context, bool) error
	DeviceInfo(context.Context) (DeviceInfo, error)
	ArtMode(context.Context) (string, error)
	Inventory(context.Context) (json.RawMessage, error)
	Close(context.Context) error
}

// adapter owns observation, guarded mutation, authorization issuance, backoff,
// and runtime status for one TV. Callers depend on the narrow Adapter contract.
type adapter struct {
	config    Config
	clock     Clock
	random    io.Reader
	logger    *slog.Logger
	transport observationTransport

	operationMu          sync.Mutex
	stateMu              sync.Mutex
	closed               bool
	connectionGeneration uint64
	runtime              adapterRuntime
}

type adapterRuntime struct {
	InventoryFingerprint [sha256.Size]byte
	ConsecutiveFailures  int
	NextAttemptAt        time.Time
	CycleID              string
	CollectionGeneration string
}

var _ Adapter = (*adapter)(nil)

// NewAdapter constructs the guarded production adapter for one TV.
func NewAdapter(config Config, dependencies Dependencies) (*adapter, error) {
	config = normalizeAdapterConfig(config)
	return newAdapter(config, dependencies, newProtocolTransport(config, dependencies))
}

func newAdapter(
	config Config,
	dependencies Dependencies,
	transport observationTransport,
) (*adapter, error) {
	config = normalizeAdapterConfig(config)
	if err := validateAdapterConfig(config); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("samsung adapter observation transport is required")
	}
	if dependencies.Clock == nil {
		dependencies.Clock = wallClock{}
	}
	if dependencies.Random == nil {
		dependencies.Random = cryptorand.Reader
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.Default()
	}
	return &adapter{
		config:    config,
		clock:     dependencies.Clock,
		random:    dependencies.Random,
		logger:    dependencies.Logger.With("component", "samsung-tv"),
		transport: transport,
	}, nil
}

func normalizeAdapterConfig(config Config) Config {
	config.Address = strings.TrimSpace(config.Address)
	config.ClientName = strings.TrimSpace(config.ClientName)
	config.TokenPath = strings.TrimSpace(config.TokenPath)
	config.MAC = slices.Clone(config.MAC)
	return config
}

func validateAdapterConfig(config Config) error {
	if config.Address == "" {
		return errors.New("samsung adapter address is required")
	}
	if config.ClientName == "" {
		return errors.New("samsung adapter client name is required")
	}
	if config.TokenPath == "" {
		return errors.New("samsung adapter token path is required")
	}
	if config.ConnectTimeout <= 0 || config.RequestTimeout <= 0 || config.GateTimeout <= 0 {
		return errors.New("samsung adapter timeouts must be positive")
	}
	if config.BackoffBase <= 0 || config.BackoffMaximum <= 0 {
		return errors.New("samsung adapter backoff durations must be positive")
	}
	if config.BackoffMaximum > maxBackoffDelay {
		return fmt.Errorf("samsung adapter maximum backoff exceeds %s", maxBackoffDelay)
	}
	if config.BackoffBase > config.BackoffMaximum {
		return errors.New("samsung adapter base backoff exceeds maximum")
	}
	return nil
}

// Observe returns only facts obtained successfully during this call. It never
// wakes the TV or sends any TV mutation.
//
//nolint:funlen // The linear fail-closed state sequence mirrors the protocol safety matrix.
func (a *adapter) Observe(ctx context.Context, request ObserveRequest) (Observation, error) {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()

	now := a.clock.Now()
	observation := Observation{
		TV:          TVIdentity{Address: a.config.Address},
		Connection:  ConnectionDisconnected,
		ObservedAt:  now,
		Disposition: DispositionUnsafeUnknown,
	}
	a.stateMu.Lock()
	closed := a.closed
	nextAttemptAt := a.runtime.NextAttemptAt
	consecutiveFailures := a.runtime.ConsecutiveFailures
	a.stateMu.Unlock()
	if closed {
		return observation, fmt.Errorf("observe closed adapter: %w", ErrNotConnected)
	}
	if err := ctx.Err(); err != nil {
		return a.failObservation(ctx, observation, request, operationObserve, err)
	}
	if now.Before(nextAttemptAt) {
		observation.Connection = ConnectionBackingOff
		observation.Disposition = DispositionBlockedBackoff
		err := &Error{
			Kind:                ErrorKindBackoff,
			Operation:           operationObserve,
			Retryable:           true,
			Outcome:             OutcomeNotAttempted,
			RetryAt:             nextAttemptAt,
			ConsecutiveFailures: consecutiveFailures,
			Cause:               fmt.Errorf("retry after %s", nextAttemptAt.Format(time.RFC3339Nano)),
		}
		a.publishAuthorizationFacts(observation, request)
		return cloneObservation(observation), err
	}

	observation.Connection = ConnectionConnecting
	if err := a.transport.Connect(ctx, request.DryRun); err != nil {
		if errors.Is(err, ErrGateFailed) {
			observation.Connection = ConnectionDisconnected
			observation.Disposition = DispositionBlockedQuietGate
			return a.settleObservation(observation, request), nil
		}
		return a.failObservation(ctx, observation, request, "connect", err)
	}
	a.stateMu.Lock()
	a.connectionGeneration++
	connectionGeneration := a.connectionGeneration
	a.stateMu.Unlock()
	observation.Connection = ConnectionReady

	device, err := a.transport.DeviceInfo(ctx)
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe device info", err)
	}
	if err := validateFrameTVDevice(device); err != nil {
		return a.failObservation(ctx, observation, request, "validate device info", err)
	}
	observation.TV = TVIdentity{
		Address:         a.config.Address,
		Model:           strings.TrimSpace(device.ModelName),
		FirmwareVersion: strings.TrimSpace(device.FirmwareVersion),
		Known:           strings.TrimSpace(device.ModelName) != "",
	}
	state, err := readOperationalState(ctx, a.transport, device)
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe operational state", err)
	}
	observation.Power = state.Power
	observation.ArtMode = state.ArtMode
	if state.ArtMode != ArtModeUnknown {
		observation.Capabilities.ArtStateObservation = SupportSupported
	}
	if state.Power == PowerStateOff {
		return a.knownPowerOffObservation(observation, request, connectionGeneration), nil
	}
	if state.ArtMode == ArtModeOff {
		observation.Disposition = DispositionBlockedNotArtMode
		return a.settleObservation(observation, request), nil
	}

	rawInventory, err := a.transport.Inventory(ctx)
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe inventory", err)
	}
	inventory, err := normalizeInventory(rawInventory, now)
	if err != nil {
		return a.failObservation(ctx, observation, request, "observe inventory", err)
	}
	observation.Inventory = inventory
	observation.Capabilities.UserArtInventory = SupportSupported
	if err := a.observeMutationCapabilities(ctx, request, &observation); err != nil {
		return a.failObservation(ctx, observation, request, "observe command capabilities", err)
	}
	if !observation.Capabilities.supports(request.Required) {
		a.publishAuthorizationFacts(observation, request)
		return cloneObservation(observation), nil
	}

	observation.Disposition = DispositionEligible
	observation.Authorization = a.newAuthorization(request, connectionGeneration, inventory.Fingerprint)
	return a.settleObservation(observation, request), nil
}

// settleObservation completes a successful observation: it clears backoff and
// publishes the facts a later authorization must match.
func (a *adapter) settleObservation(observation Observation, request ObserveRequest) Observation {
	a.resetFailures()
	a.publishAuthorizationFacts(observation, request)
	return cloneObservation(observation)
}

func (a *adapter) knownPowerOffObservation(
	observation Observation,
	request ObserveRequest,
	connectionGeneration uint64,
) Observation {
	observation.Disposition = DispositionBlockedPowerOff
	if _, supportsMutation := a.transport.(mutationTransport); supportsMutation &&
		request.Required == CapabilityRemotePower && len(a.config.MAC) == 6 {
		observation.Capabilities.RemotePower = SupportSupported
		observation.Authorization = a.newAuthorization(request, connectionGeneration, [sha256.Size]byte{})
	}
	return a.settleObservation(observation, request)
}

// Close invalidates all issued authorizations and releases the read channel.
func (a *adapter) Close(ctx context.Context) error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()

	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		return nil
	}
	a.closed = true
	a.connectionGeneration++
	a.stateMu.Unlock()

	closeCtx, cancel := adapterCloseContext(ctx, a.config.RequestTimeout)
	defer cancel()
	if err := a.transport.Close(closeCtx); err != nil {
		return fmt.Errorf("close Samsung observation transport: %w", err)
	}
	return nil
}

func (a *adapter) newAuthorization(
	request ObserveRequest,
	connectionGeneration uint64,
	inventoryFingerprint [sha256.Size]byte,
) Authorization {
	return Authorization{
		adapter:              a,
		connectionGeneration: connectionGeneration,
		cycleID:              request.CycleID,
		collectionGeneration: request.CollectionGeneration,
		inventoryFingerprint: inventoryFingerprint,
		required:             request.Required,
		dryRun:               request.DryRun,
	}
}

func (a *adapter) observeMutationCapabilities(
	ctx context.Context,
	request ObserveRequest,
	observation *Observation,
) error {
	transport, ok := a.transport.(mutationTransport)
	if !ok {
		return nil
	}
	observation.Capabilities.ImageUpload = SupportSupported
	observation.Capabilities.ImageDeletion = SupportSupported
	// Samsung's art API does not expose a reliable current-selection readback,
	// so selection cannot be authorized with a verifiable postcondition.
	observation.Capabilities.ImageSelection = SupportUnsupported
	observation.Capabilities.RemotePower = SupportSupported
	if request.Required&(CapabilitySlideshowRead|CapabilitySlideshowWrite) != 0 {
		value, err := transport.Slideshow(ctx)
		if err != nil {
			return err
		}
		observation.Slideshow = SlideshowObservation{Setting: value, Known: true, ObservedAt: observation.ObservedAt}
		observation.Capabilities.SlideshowRead = SupportSupported
		observation.Capabilities.SlideshowWrite = SupportSupported
	}
	if request.Required&(CapabilityBrightnessRead|CapabilityBrightnessWrite) != 0 {
		value, err := transport.Brightness(ctx)
		if err != nil {
			return err
		}
		observation.Brightness = SettingObservation{Value: value, Known: true, ObservedAt: observation.ObservedAt}
		observation.Capabilities.BrightnessRead = SupportSupported
		observation.Capabilities.BrightnessWrite = SupportSupported
	}
	return nil
}

func adapterCloseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (a *adapter) failObservation(
	ctx context.Context,
	observation Observation,
	request ObserveRequest,
	operation string,
	cause error,
) (Observation, error) {
	typed := classifyObservationError(operation, cause)
	observation.Disposition = DispositionUnsafeUnknown
	observation.ArtMode = ArtModeUnknown
	observation.Authorization = Authorization{}
	if typed.Kind == ErrorKindCanceled {
		a.publishAuthorizationFacts(observation, request)
		return cloneObservation(observation), typed
	}
	if typed.Kind == ErrorKindUnauthorized {
		observation.Connection = ConnectionAuthRequired
	} else if typed.Retryable {
		a.stateMu.Lock()
		a.runtime.ConsecutiveFailures++
		a.runtime.NextAttemptAt = a.clock.Now().Add(a.backoffDelay(a.runtime.ConsecutiveFailures))
		typed.ConsecutiveFailures = a.runtime.ConsecutiveFailures
		typed.RetryAt = a.runtime.NextAttemptAt
		a.stateMu.Unlock()
	}
	if closeErr := a.transport.Close(context.WithoutCancel(ctx)); closeErr != nil {
		a.logger.Debug("close failed after observation error", "error", closeErr)
	}
	a.stateMu.Lock()
	a.connectionGeneration++
	a.stateMu.Unlock()
	a.publishAuthorizationFacts(observation, request)
	return cloneObservation(observation), typed
}

func classifyObservationError(operation string, cause error) *Error {
	kind := ErrorKindInvalidResponse
	retryable := true
	switch {
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		kind = ErrorKindCanceled
		retryable = false
	case errors.Is(cause, ErrUnauthorized):
		kind = ErrorKindUnauthorized
		retryable = false
	case errors.Is(cause, ErrTimeout):
		kind = ErrorKindTimeout
	case errors.Is(cause, ErrConnectionFailure), errors.Is(cause, ErrNotConnected):
		kind = ErrorKindUnreachable
	}
	var typed *Error
	if errors.As(cause, &typed) {
		kind = typed.Kind
		retryable = typed.Retryable
	}
	return &Error{
		Kind:      kind,
		Operation: operation,
		Retryable: retryable,
		Outcome:   OutcomeNotAttempted,
		Cause:     cause,
	}
}

func (a *adapter) backoffDelay(failures int) time.Duration {
	limit := a.config.BackoffBase
	for failure := 1; failure < failures && limit < a.config.BackoffMaximum; failure++ {
		if limit > a.config.BackoffMaximum/2 {
			limit = a.config.BackoffMaximum
			break
		}
		limit *= 2
	}
	var randomBytes [8]byte
	if _, err := io.ReadFull(a.random, randomBytes[:]); err != nil {
		a.logger.Debug("backoff randomness unavailable; using maximum", "error", err)
		return limit
	}
	value := binary.BigEndian.Uint64(randomBytes[:])
	if value == math.MaxUint64 {
		return limit
	}
	return time.Duration((float64(value) / float64(math.MaxUint64)) * float64(limit))
}

func (a *adapter) resetFailures() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.runtime.ConsecutiveFailures = 0
	a.runtime.NextAttemptAt = time.Time{}
}

func (a *adapter) publishAuthorizationFacts(observation Observation, request ObserveRequest) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.runtime.InventoryFingerprint = observation.Inventory.Fingerprint
	a.runtime.CycleID = request.CycleID
	a.runtime.CollectionGeneration = request.CollectionGeneration
}

func normalizeInventory(raw json.RawMessage, observedAt time.Time) (Inventory, error) {
	content, err := parseUploadedContent(raw)
	if err != nil {
		return Inventory{}, err
	}
	ids := make([]string, 0, len(content))
	for _, item := range content {
		ids = append(ids, item.ContentID)
	}
	slices.Sort(ids)
	canonical, err := json.Marshal(ids)
	if err != nil {
		return Inventory{}, fmt.Errorf("encode canonical inventory: %w", err)
	}
	return Inventory{
		CategoryID:  userArtCategory,
		ContentIDs:  ids,
		Fingerprint: sha256.Sum256(canonical),
		Known:       true,
		ObservedAt:  observedAt,
	}, nil
}

func cloneObservation(observation Observation) Observation {
	observation.Inventory.ContentIDs = slices.Clone(observation.Inventory.ContentIDs)
	return observation
}
