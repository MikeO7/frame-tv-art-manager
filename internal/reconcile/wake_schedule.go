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
	"os"
	"path/filepath"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// wakeRecord is the durable proof that the scheduled wake for one boundary was
// already handled, so a TV the owner turns off again is not woken every cycle.
type wakeRecord struct {
	Boundary time.Time `json:"boundary"`
}

func (s stateStore) wakePath(address string) string {
	key := sha256.Sum256([]byte(address))
	return filepath.Join(s.directory, "tv_"+hex.EncodeToString(key[:16])+"_wake.json")
}

func (s stateStore) loadWake(address string) (time.Time, error) {
	data, err := os.ReadFile(s.wakePath(address))
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read wake record: %w", err)
	}
	var record wakeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return time.Time{}, fmt.Errorf("parse wake record: %w", err)
	}
	return record.Boundary, nil
}

func (s stateStore) saveWake(ctx context.Context, address string, boundary time.Time) error {
	if err := s.validateDirectory(true); err != nil {
		return err
	}
	data, err := json.Marshal(wakeRecord{Boundary: boundary})
	if err != nil {
		return fmt.Errorf("encode wake record: %w", err)
	}
	if err := s.replace(ctx, s.wakePath(address), 0o600, func(writer io.Writer) error {
		_, writeErr := writer.Write(data)
		return writeErr
	}); err != nil {
		return fmt.Errorf("persist wake record: %w", err)
	}
	return nil
}

// scheduledWake handles a power-on policy tied to a wake boundary. The TV is
// woken at most once per boundary: the boundary is recorded when the TV is
// already on, and when a wake packet is sent. The wake runs before identity
// and state are loaded because a TV in deep standby answers nothing but
// Wake-on-LAN. The returned policy no longer asks for power, so the rest of
// the run converges a TV that is on and skips one that stays off.
func (s *service) scheduledWake(
	ctx context.Context,
	request Request,
	policy Policy,
	observation samsung.Observation,
) (wakeStep, error) {
	boundary := policy.WakeBoundary
	policy.Power, policy.WakeBoundary = PowerPreserve, time.Time{}
	step := wakeStep{policy: policy, observation: observation}
	address := observation.TV.Address
	if address == "" {
		return step, errors.New("scheduled wake requires the TV address")
	}
	handled, err := s.store.loadWake(address)
	if err != nil {
		return step, err
	}
	if !handled.Before(boundary) {
		return s.reobserve(ctx, request, step, nil)
	}
	switch {
	case observation.Power == samsung.PowerStateOn:
		if request.DryRun {
			return s.reobserve(ctx, request, step, nil)
		}
		return s.reobserve(ctx, request, step, s.store.saveWake(ctx, address, boundary))
	case observation.Power != samsung.PowerStateOff && !observation.PresumedStandby:
		// Unknown power never authorizes a wake.
		return step, nil
	case observation.Capabilities.RemotePower != samsung.SupportSupported:
		return step, fmt.Errorf("%w: wake requires a configured MAC address", ErrUnsupportedIntent)
	case request.DryRun:
		return step, nil
	}
	receipt, wakeErr := request.TV.Apply(ctx, observation.Authorization, samsung.Wake{})
	if receipt.Outcome == samsung.OutcomeNotAttempted {
		return step, fmt.Errorf("scheduled wake: %w", wakeErr)
	}
	s.logger.Info("scheduled wake sent", "cycle_id", request.CycleID,
		"boundary", boundary, "outcome", receipt.Outcome.String())
	if err := s.store.saveWake(ctx, address, boundary); err != nil {
		step.applied = []CommandKind{CommandWake}
		return step, errors.Join(wakeErr, err)
	}
	if wakeErr != nil {
		step.applied = []CommandKind{CommandWake}
		return step, fmt.Errorf("scheduled wake: %w", wakeErr)
	}
	step.applied = []CommandKind{CommandWake}
	return s.reobserve(ctx, request, step, nil)
}

// reobserve replaces the wake-only observation with one that supports
// convergence, after the wake step finished without error.
func (s *service) reobserve(ctx context.Context, request Request, step wakeStep, stepErr error) (wakeStep, error) {
	if stepErr != nil {
		return step, stepErr
	}
	fresh, err := request.TV.Observe(ctx, samsung.ObserveRequest{
		CycleID: request.CycleID, CollectionGeneration: request.Snapshot.Generation,
		DryRun: request.DryRun, Required: convergenceObservationCapabilities(step.policy),
		Optional: settingCapabilities(step.policy),
	})
	step.observation = fresh
	return step, err
}

// wakeStep is the outcome of the scheduled wake: the policy for the rest of
// the run, the observation to plan from, and the wake if one was sent.
type wakeStep struct {
	policy      Policy
	observation samsung.Observation
	applied     []CommandKind
}
