package reconcile

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// uploadSettleTime is how long an upload whose outcome is unknown may still
// appear in inventory. Frame TVs finish storing an image within seconds of the
// transfer, so an upload that has not appeared after this did not land.
const uploadSettleTime = 5 * time.Minute

// uploadParkLimit bounds how long an upload stays parked when inventory can
// never prove it: the image landed under an ID the TV did not report, or the
// owner added a picture of their own, so some artwork stays unexplained for
// good. After this the park is given up and artwork changes resume.
const uploadParkLimit = 24 * time.Hour

// uploadSettlement is what a fresh inventory says about a parked upload.
type uploadSettlement uint8

const (
	// uploadStillParked: the outcome is unproven and the park is kept.
	uploadStillParked uploadSettlement = iota
	// uploadSettled: inventory proved the upload landed or did not.
	uploadSettled
	// uploadParkExpired: nothing proved the outcome within uploadParkLimit.
	uploadParkExpired
)

// parkableUpload reports whether recovery stopped on an upload outcome that a
// later inventory can still prove.
func parkableUpload(state State, err error) bool {
	return errors.Is(err, ErrRecoveryRequired) && state.Pending != nil &&
		state.Pending.Command.Kind == CommandUpload && state.Pending.Phase != phaseApplied &&
		state.UnresolvedUpload == nil
}

// parkUpload moves an unproven upload out of Pending so that settings and
// power control keep working while artwork changes wait for proof.
func (s *service) parkUpload(ctx context.Context, cycleID string, dryRun bool, state State) (State, error) {
	parked := cloneState(state)
	parked.UnresolvedUpload = parked.Pending
	parked.Pending = nil
	s.logger.Warn("upload outcome is unknown; pausing artwork changes until inventory proves it",
		"cycle_id", cycleID, "operation_id", parked.UnresolvedUpload.OperationID,
		"artwork", parked.UnresolvedUpload.Command.Name)
	if dryRun {
		return parked, nil
	}
	parked.Revision++
	if err := s.store.save(ctx, parked); err != nil {
		return state, err
	}
	return parked, nil
}

// settleUnresolvedUpload tries to prove a parked upload from a fresh
// inventory and persists the outcome when it can.
func (s *service) settleUnresolvedUpload(
	ctx context.Context,
	cycleID string,
	dryRun bool,
	observation samsung.Observation,
	state State,
) (State, error) {
	resolved, settlement := resolveUnresolvedUpload(state, observation, s.clock.Now())
	switch settlement {
	case uploadStillParked:
		return state, nil
	case uploadParkExpired:
		s.logger.Warn("upload outcome was never proven; resuming artwork changes, the artwork may be uploaded again",
			"cycle_id", cycleID, "operation_id", state.UnresolvedUpload.OperationID,
			"artwork", state.UnresolvedUpload.Command.Name, "parked_for_at_least", uploadParkLimit.String())
	case uploadSettled:
		s.logger.Info("unresolved upload settled from inventory", "cycle_id", cycleID,
			"operation_id", state.UnresolvedUpload.OperationID,
			"bound", len(resolved.Bindings) > len(state.Bindings))
	}
	if dryRun {
		return resolved, nil
	}
	resolved.Revision++
	if err := s.store.save(ctx, resolved); err != nil {
		return state, err
	}
	return resolved, nil
}

// resolveUnresolvedUpload settles a parked upload when inventory proves its
// outcome. A content ID from the receipt is decisive either way. Without one,
// the upload is proven absent only when no unexplained artwork has appeared
// since it was prepared and the settle time has passed. Any new, unbound
// artwork keeps it parked: binding the wrong image could later delete
// someone else's art. Such artwork may never go away, so the park expires
// after uploadParkLimit: the upload is dropped without a binding and the
// unexplained artwork stays unknown art.
func resolveUnresolvedUpload(state State, observation samsung.Observation, now time.Time) (State, uploadSettlement) {
	parked := state.UnresolvedUpload
	if parked == nil || !observation.Inventory.Known {
		return state, uploadStillParked
	}
	present := make(map[string]struct{}, len(observation.Inventory.ContentIDs))
	for _, id := range observation.Inventory.ContentIDs {
		present[id] = struct{}{}
	}
	result := cloneState(state)
	result.UnresolvedUpload = nil
	if parked.Receipt != nil {
		if contentID := strings.TrimSpace(parked.Receipt.ContentID); contentID != "" {
			if _, landed := present[contentID]; landed {
				proven := clonePending(parked)
				proven.Receipt.ContentID = contentID
				foldUploadBinding(&result, proven, observation, now)
			}
			return result, uploadSettled
		}
	}
	if settleAt := uploadSettleStart(parked); settleAt != nil && now.Sub(*settleAt) < uploadSettleTime {
		return state, uploadStillParked
	}
	if !unexplainedArtwork(state, parked, observation) {
		return result, uploadSettled
	}
	// A state file written before PreparedAt existed cannot say how long the
	// upload has been parked, so it is treated as expired.
	if parked.PreparedAt == nil || now.Sub(*parked.PreparedAt) >= uploadParkLimit {
		return result, uploadParkExpired
	}
	return state, uploadStillParked
}

func uploadSettleStart(parked *pending) *time.Time {
	if parked.Receipt != nil && !parked.Receipt.CompletedAt.IsZero() {
		return &parked.Receipt.CompletedAt
	}
	return parked.PreparedAt
}

// unexplainedArtwork reports whether inventory holds artwork that could be the
// parked upload: an ID absent before the upload and not bound to other art.
// State files written before InventoryBeforeIDs existed fall back to the
// inventory fingerprint, so any change at all stays unexplained.
func unexplainedArtwork(state State, parked *pending, observation samsung.Observation) bool {
	if parked.InventoryBeforeIDs == nil {
		return observation.Inventory.Fingerprint != parked.InventoryBefore.Digest
	}
	known := make(map[string]struct{}, len(parked.InventoryBeforeIDs)+len(state.Bindings))
	for _, id := range parked.InventoryBeforeIDs {
		known[id] = struct{}{}
	}
	for _, binding := range state.Bindings {
		known[binding.ContentID] = struct{}{}
	}
	for _, id := range observation.Inventory.ContentIDs {
		if _, explained := known[id]; !explained {
			return true
		}
	}
	return false
}

// artworkCommandsBlocked removes commands that change artwork while an upload
// outcome is unproven. Settings and power commands are kept.
func artworkCommandsBlocked(commands []commandIntent) []commandIntent {
	for _, kind := range []CommandKind{CommandUpload, CommandDeleteOwned, CommandDeleteUnknown, commandSelect} {
		commands = withoutCommand(commands, kind)
	}
	return commands
}
