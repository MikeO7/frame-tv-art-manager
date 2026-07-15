package reconcile

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type initialStateInput struct {
	request     Request
	policy      Policy
	observation samsung.Observation
	identity    TVIdentity
	state       State
	exists      bool
}

func (s *service) prepareInitialState(ctx context.Context, input initialStateInput) (State, error) {
	if input.exists {
		input.state.TV = input.identity
		return input.state, nil
	}
	if input.policy.Power != PowerOn {
		return s.stateForFirstRun(
			ctx, input.identity, input.request.Snapshot, input.observation, input.request.DryRun,
		)
	}
	state := initialState(input.identity)
	state.LegacyMigrationPending = s.legacy.configured()
	return state, nil
}

func (s *service) stateForFirstRun(
	ctx context.Context,
	identity TVIdentity,
	snapshot collection.Snapshot,
	observation samsung.Observation,
	dryRun bool,
) (State, error) {
	state := initialState(identity)
	mapping, exists, err := s.legacy.load(ctx, identity.Address)
	if err != nil || !exists {
		return state, err
	}
	if err := validateObservation(observation); err != nil {
		return state, fmt.Errorf("migrate legacy mapping: %w", err)
	}
	bindings, ignoredEntries, err := projectLegacyMapping(
		snapshot, observation, mapping, s.clock.Now(),
	)
	if err != nil {
		return state, err
	}
	state.Bindings = bindings
	if dryRun {
		return state, nil
	}
	if err := s.store.save(ctx, state); err != nil {
		return state, fmt.Errorf("persist migrated legacy mapping: %w", err)
	}
	s.logger.InfoContext(ctx, "legacy mapping migrated",
		"tv", identity.Address,
		"bindings", len(state.Bindings),
		"ignored_entries", ignoredEntries,
	)
	return state, nil
}

func (s *service) completeDeferredLegacyMigration(
	ctx context.Context,
	state State,
	snapshot collection.Snapshot,
	observation samsung.Observation,
	dryRun bool,
) (State, error) {
	if !state.LegacyMigrationPending {
		return state, nil
	}
	if state.Pending != nil || len(state.Bindings) != 0 {
		return state, errors.New("deferred legacy migration requires idle empty reconciliation state")
	}
	mapping, exists, err := s.legacy.load(ctx, state.TV.Address)
	if err != nil {
		return state, err
	}
	bindings := map[string]Binding{}
	ignoredEntries := 0
	if exists {
		if err := validateObservation(observation); err != nil {
			return state, fmt.Errorf("migrate legacy mapping: %w", err)
		}
		bindings, ignoredEntries, err = projectLegacyMapping(snapshot, observation, mapping, s.clock.Now())
		if err != nil {
			return state, err
		}
	}
	migrated := cloneState(state)
	migrated.Bindings = bindings
	migrated.LegacyMigrationPending = false
	if dryRun {
		return migrated, nil
	}
	migrated.Revision++
	if err := s.store.save(ctx, migrated); err != nil {
		return migrated, fmt.Errorf("persist deferred legacy mapping migration: %w", err)
	}
	s.logger.InfoContext(ctx, "deferred legacy mapping migrated",
		"tv", state.TV.Address,
		"bindings", len(bindings),
		"ignored_entries", ignoredEntries,
	)
	return migrated, nil
}

func projectLegacyMapping(
	snapshot collection.Snapshot,
	observation samsung.Observation,
	mapping map[string]string,
	confirmedAt time.Time,
) (map[string]Binding, int, error) {
	items := make(map[string]collection.Item, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items[item.Name] = item
	}
	inventory := make(map[string]struct{}, len(observation.Inventory.ContentIDs))
	for _, contentID := range observation.Inventory.ContentIDs {
		inventory[contentID] = struct{}{}
	}
	usedDigests := make(map[string]string)
	usedContentIDs := make(map[string]string)
	ignoredEntries := 0
	bindings := make(map[string]Binding)
	for name, contentID := range mapping {
		item, inCollection := items[name]
		_, onTV := inventory[contentID]
		if !inCollection || !onTV {
			ignoredEntries++
			continue
		}
		digest := hex.EncodeToString(item.Digest[:])
		if previous, duplicate := usedDigests[digest]; duplicate {
			return nil, 0, fmt.Errorf("migrate legacy mapping: digest %s belongs to both %q and %q", digest, previous, name)
		}
		if previous, duplicate := usedContentIDs[contentID]; duplicate {
			return nil, 0, fmt.Errorf("migrate legacy mapping: content ID %q belongs to both %q and %q", contentID, previous, name)
		}
		usedDigests[digest] = name
		usedContentIDs[contentID] = name
		bindings[digest] = Binding{
			Digest: digest, ContentID: contentID, Name: name, Size: item.Size,
			CollectionGeneration: snapshot.Generation, ConfirmedAt: confirmedAt,
		}
	}
	return bindings, ignoredEntries, nil
}
