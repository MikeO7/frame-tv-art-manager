package reconcile

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// BuildPlan is a pure deterministic planner. It never reads files, observes a
// TV, or mutates its inputs.
func BuildPlan(snapshot collection.Snapshot, observation samsung.Observation, state State, policy Policy) (Plan, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return Plan{}, err
	}
	return planSnapshot(snapshot, observation, state, policy)
}

// planSnapshot is BuildPlan for a snapshot that Run has already validated. The
// fresh observation and in-memory state are still validated on every call.
//
//nolint:funlen,gocognit,nestif // The linear phases deliberately mirror reconciliation ordering.
func planSnapshot(snapshot collection.Snapshot, observation samsung.Observation, state State, policy Policy) (Plan, error) {
	policy = normalizePolicy(policy)
	identity, err := validateObservation(observation)
	if err != nil {
		return Plan{}, err
	}
	if err := validateState(state, identity); err != nil {
		return Plan{}, err
	}
	if state.Pending != nil {
		return Plan{}, ErrRecoveryRequired
	}
	policyFingerprint, err := fingerprintPolicy(policy)
	if err != nil {
		return Plan{}, err
	}
	matteOverrides, err := policy.MatteOverrides.index()
	if err != nil {
		return Plan{}, fmt.Errorf("index matte overrides: %w", err)
	}
	plan := Plan{
		CollectionGeneration: snapshot.Generation,
		PolicyFingerprint:    policyFingerprint,
		InventoryFingerprint: observation.Inventory.Fingerprint,
	}
	inventory := make(map[string]struct{}, len(observation.Inventory.ContentIDs))
	for _, contentID := range observation.Inventory.ContentIDs {
		inventory[contentID] = struct{}{}
	}
	desired := make(map[string]collection.Item, len(snapshot.Items))
	for _, item := range snapshot.Items {
		desired[hex.EncodeToString(item.Digest[:])] = item
	}

	boundContent := make(map[string]string, len(state.Bindings))
	obsolete := make([]Binding, 0)
	owned := make([]Binding, 0, len(state.Bindings))
	for digest, binding := range state.Bindings {
		if _, present := inventory[binding.ContentID]; !present {
			plan.PruneBindings = append(plan.PruneBindings, digest)
			continue
		}
		boundContent[binding.ContentID] = digest
		if _, wanted := desired[digest]; wanted {
			owned = append(owned, binding)
		} else {
			obsolete = append(obsolete, binding)
		}
	}
	slices.Sort(plan.PruneBindings)
	slices.SortFunc(obsolete, func(left, right Binding) int {
		if order := strings.Compare(left.Digest, right.Digest); order != 0 {
			return order
		}
		return strings.Compare(left.ContentID, right.ContentID)
	})

	waiting := make([]waitingArtwork, 0, len(desired)-len(owned))
	for digest, item := range desired {
		if binding, exists := state.Bindings[digest]; exists {
			if _, present := inventory[binding.ContentID]; present {
				continue
			}
		}
		waiting = append(waiting, waitingArtwork{item: item, digest: digest, lastShown: state.LastShown[digest]})
	}

	// Never remove the final known-good owned item unless a successor is already
	// bound, or policy explicitly permits an empty managed collection.
	boundDesired := len(owned)
	ownedRemaining := len(state.Bindings) - len(plan.PruneBindings)
	for _, binding := range obsolete {
		if !policy.AllowEmpty && boundDesired == 0 && ownedRemaining <= 1 {
			continue
		}
		plan.Commands = append(plan.Commands, CommandIntent{
			Kind: CommandDeleteOwned, Digest: binding.Digest, ContentID: binding.ContentID,
		})
		ownedRemaining--
	}
	admission := planCapacity(capacityInput{
		waiting: waiting, owned: owned, state: state, inventory: len(observation.Inventory.ContentIDs),
		policy: policy, now: observation.Inventory.ObservedAt,
	})
	plan.CapacityLimited = admission.limited
	plan.Rotation = admission.rotation
	upload := func(artwork waitingArtwork) CommandIntent {
		matte := policy.DefaultMatte
		if override, exists := matteOverrides[artwork.item.Name]; exists {
			matte = override
		}
		return CommandIntent{
			Kind: CommandUpload, Digest: artwork.digest, Name: artwork.item.Name,
			Path: artwork.item.Path, FileType: artwork.item.Type, Size: artwork.item.Size, Matte: matte,
		}
	}
	for _, artwork := range admission.fills {
		plan.Commands = append(plan.Commands, upload(artwork))
	}
	for _, swap := range admission.swaps {
		plan.Commands = append(plan.Commands,
			CommandIntent{Kind: CommandDeleteOwned, Digest: swap.evict.Digest, ContentID: swap.evict.ContentID},
			upload(swap.upload),
		)
	}
	if policy.RemoveUnknown {
		for _, contentID := range observation.Inventory.ContentIDs {
			if _, owned := boundContent[contentID]; owned {
				continue
			}
			plan.Commands = append(plan.Commands, CommandIntent{
				Kind: CommandDeleteUnknown, ContentID: contentID, RemoveUnknownApproved: true,
			})
		}
	}
	if policy.Select {
		digests := make([]string, 0, len(desired))
		for digest := range desired {
			digests = append(digests, digest)
		}
		slices.Sort(digests)
		for _, digest := range digests {
			binding, bound := state.Bindings[digest]
			if _, present := inventory[binding.ContentID]; bound && present {
				plan.Commands = append(plan.Commands, CommandIntent{Kind: CommandSelect, ContentID: binding.ContentID})
				break
			}
		}
	}
	if policy.Slideshow.Mode != PolicyPreserve {
		if !observation.Slideshow.Known || !observation.Slideshow.Setting.Valid() {
			return Plan{}, fmt.Errorf("slideshow observation is unavailable: %w", ErrUnsupportedIntent)
		}
		desired := policy.Slideshow.Setting
		if policy.Slideshow.Mode == PolicyDisable {
			desired.Interval = 0
			if !desired.Valid() {
				desired.Kind = observation.Slideshow.Setting.Kind
			}
		}
		if observation.Slideshow.Setting != desired {
			previous := observation.Slideshow.Setting
			plan.Commands = append(plan.Commands, CommandIntent{
				Kind: CommandSlideshow, PreviousSlideshow: &previous, DesiredSlideshow: &desired,
			})
		}
	}
	if policy.Brightness.Mode != PolicyPreserve {
		if !observation.Brightness.Known {
			return Plan{}, fmt.Errorf("brightness observation is unavailable: %w", ErrUnsupportedIntent)
		}
		if observation.Brightness.Value != policy.Brightness.Value {
			previous, desired := observation.Brightness.Value, policy.Brightness.Value
			plan.Commands = append(plan.Commands, CommandIntent{
				Kind: CommandBrightness, PreviousValue: &previous, DesiredValue: &desired,
			})
		}
	}
	if policy.Power == PowerOff {
		plan.Commands = append(plan.Commands, CommandIntent{Kind: CommandPowerOff})
	}
	return plan, nil
}

func initialState(identity TVIdentity) State {
	return State{
		Version:    stateVersion,
		TV:         identity,
		Revision:   1,
		Bindings:   map[string]Binding{},
		Tombstones: map[string]Tombstone{},
	}
}

func cloneState(state State) State {
	clone := state
	clone.Bindings = make(map[string]Binding, len(state.Bindings))
	maps.Copy(clone.Bindings, state.Bindings)
	clone.Tombstones = make(map[string]Tombstone, len(state.Tombstones))
	maps.Copy(clone.Tombstones, state.Tombstones)
	clone.LastShown = maps.Clone(state.LastShown)
	if state.Pending != nil {
		pending := *state.Pending
		pending.Command.PreviousSlideshow = clonePointer(pending.Command.PreviousSlideshow)
		pending.Command.DesiredSlideshow = clonePointer(pending.Command.DesiredSlideshow)
		pending.Command.PreviousValue = clonePointer(pending.Command.PreviousValue)
		pending.Command.DesiredValue = clonePointer(pending.Command.DesiredValue)
		pending.Receipt = clonePointer(pending.Receipt)
		clone.Pending = &pending
	}
	return clone
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
