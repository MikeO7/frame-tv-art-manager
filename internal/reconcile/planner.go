package reconcile

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// BuildPlan is a pure deterministic planner. It never reads files, observes a
// TV, or mutates its inputs.
//
//nolint:funlen,gocognit,gocyclo,nestif // The linear phases deliberately mirror reconciliation ordering.
func BuildPlan(snapshot collection.Snapshot, observation samsung.Observation, state State, policy Policy) (Plan, error) {
	policy = normalizePolicy(policy)
	if err := validateSnapshot(snapshot); err != nil {
		return Plan{}, err
	}
	if err := validateObservation(observation); err != nil {
		return Plan{}, err
	}
	identity, err := identityFromObservation(observation)
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
	for digest, binding := range state.Bindings {
		if _, present := inventory[binding.ContentID]; !present {
			plan.PruneBindings = append(plan.PruneBindings, digest)
			continue
		}
		boundContent[binding.ContentID] = digest
		if _, wanted := desired[digest]; !wanted {
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

	missing := make([]collection.Item, 0)
	for digest, item := range desired {
		binding, exists := state.Bindings[digest]
		if !exists {
			missing = append(missing, item)
			continue
		}
		if _, present := inventory[binding.ContentID]; !present {
			missing = append(missing, item)
		}
	}
	slices.SortFunc(missing, func(left, right collection.Item) int {
		return strings.Compare(hex.EncodeToString(left.Digest[:]), hex.EncodeToString(right.Digest[:]))
	})

	// Never remove the final known-good owned item unless a successor is already
	// bound, or policy explicitly permits an empty managed collection.
	boundDesired := len(desired) - len(missing)
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
	if state.Capacity.Known {
		available := max(state.Capacity.Maximum-len(observation.Inventory.ContentIDs), 0)
		if len(missing) > available {
			plan.CapacityLimited = true
			missing = missing[:available]
		}
	}
	for _, item := range missing {
		matte := policy.DefaultMatte
		if override, exists := matteOverrides[item.Name]; exists {
			matte = override
		}
		plan.Commands = append(plan.Commands, CommandIntent{
			Kind: CommandUpload, Digest: hex.EncodeToString(item.Digest[:]), Name: item.Name,
			Path: item.Path, FileType: item.Type, Size: item.Size, Matte: matte,
		})
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
			if bound && containsInventoryID(observation.Inventory.ContentIDs, binding.ContentID) {
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

func containsInventoryID(ids []string, contentID string) bool {
	_, present := slices.BinarySearch(ids, contentID)
	return present
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

//nolint:nestif // Pointer fields are copied together to keep state cloning local.
func cloneState(state State) State {
	clone := state
	clone.Bindings = make(map[string]Binding, len(state.Bindings))
	for key, value := range state.Bindings {
		clone.Bindings[key] = value
	}
	clone.Tombstones = make(map[string]Tombstone, len(state.Tombstones))
	for key, value := range state.Tombstones {
		clone.Tombstones[key] = value
	}
	if state.Pending != nil {
		pending := *state.Pending
		if pending.Command.PreviousSlideshow != nil {
			value := *pending.Command.PreviousSlideshow
			pending.Command.PreviousSlideshow = &value
		}
		if pending.Command.DesiredSlideshow != nil {
			value := *pending.Command.DesiredSlideshow
			pending.Command.DesiredSlideshow = &value
		}
		if pending.Command.PreviousValue != nil {
			value := *pending.Command.PreviousValue
			pending.Command.PreviousValue = &value
		}
		if pending.Command.DesiredValue != nil {
			value := *pending.Command.DesiredValue
			pending.Command.DesiredValue = &value
		}
		if pending.Receipt != nil {
			receipt := *pending.Receipt
			pending.Receipt = &receipt
		}
		clone.Pending = &pending
	}
	return clone
}
