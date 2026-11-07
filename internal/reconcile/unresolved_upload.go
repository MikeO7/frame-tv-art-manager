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

// parkableUpload reports whether recovery stopped on an upload outcome that a
// later inventory can still prove.
func parkableUpload(state State, err error) bool {
	return errors.Is(err, ErrRecoveryRequired) && state.Pending != nil &&
		state.Pending.Command.Kind == CommandUpload && state.Pending.Phase != PhaseApplied &&
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
	resolved, settled := resolveUnresolvedUpload(state, observation, s.clock.Now())
	if !settled {
		return state, nil
	}
	s.logger.Info("unresolved upload settled from inventory", "cycle_id", cycleID,
		"operation_id", state.UnresolvedUpload.OperationID,
		"bound", len(resolved.Bindings) > len(state.Bindings))
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
// someone else's art.
func resolveUnresolvedUpload(state State, observation samsung.Observation, now time.Time) (State, bool) {
	parked := state.UnresolvedUpload
	if parked == nil || !observation.Inventory.Known {
		return state, false
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
			return result, true
		}
	}
	if settleAt := uploadSettleStart(parked); settleAt != nil && now.Sub(*settleAt) < uploadSettleTime {
		return state, false
	}
	if unexplainedArtwork(state, parked, observation) {
		return state, false
	}
	return result, true
}

func uploadSettleStart(parked *Pending) *time.Time {
	if parked.Receipt != nil && !parked.Receipt.CompletedAt.IsZero() {
		return &parked.Receipt.CompletedAt
	}
	return parked.PreparedAt
}

// unexplainedArtwork reports whether inventory holds artwork that could be the
// parked upload: an ID absent before the upload and not bound to other art.
// State files written before InventoryBeforeIDs existed fall back to the
// inventory fingerprint, so any change at all stays unexplained.
func unexplainedArtwork(state State, parked *Pending, observation samsung.Observation) bool {
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
func artworkCommandsBlocked(commands []CommandIntent) []CommandIntent {
	for _, kind := range []CommandKind{CommandUpload, CommandDeleteOwned, CommandDeleteUnknown, CommandSelect} {
		commands = withoutCommand(commands, kind)
	}
	return commands
}
