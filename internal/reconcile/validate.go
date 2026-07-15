package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func validateIdentity(identity TVIdentity) error {
	if strings.TrimSpace(identity.Address) == "" || strings.TrimSpace(identity.Model) == "" {
		return errors.New("known TV address and model are required")
	}
	if identity.Address != strings.TrimSpace(identity.Address) || identity.Model != strings.TrimSpace(identity.Model) ||
		identity.FirmwareVersion != strings.TrimSpace(identity.FirmwareVersion) {
		return errors.New("TV identity fields must be normalized")
	}
	return nil
}

func identityFromObservation(observation samsung.Observation) (TVIdentity, error) {
	if !observation.TV.Known {
		return TVIdentity{}, errors.New("TV identity is unknown")
	}
	identity := TVIdentity{
		Address:         strings.TrimSpace(observation.TV.Address),
		Model:           strings.TrimSpace(observation.TV.Model),
		FirmwareVersion: strings.TrimSpace(observation.TV.FirmwareVersion),
	}
	return identity, validateIdentity(identity)
}

//nolint:gocognit,gocyclo // State validation intentionally checks the complete durable schema.
func validateState(state State, expected TVIdentity) error {
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported reconciliation state version %d", state.Version)
	}
	if err := validateIdentity(state.TV); err != nil {
		return fmt.Errorf("invalid reconciliation TV identity: %w", err)
	}
	if !sameStableTV(state.TV, expected) {
		return errors.New("reconciliation state belongs to a different TV identity")
	}
	if state.Revision == 0 {
		return errors.New("reconciliation state revision must be positive")
	}
	if state.Bindings == nil || state.Tombstones == nil {
		return errors.New("reconciliation state maps must be present")
	}
	contentIDs := make(map[string]string, len(state.Bindings))
	for key, binding := range state.Bindings {
		if err := validateBinding(key, binding); err != nil {
			return err
		}
		if previous, exists := contentIDs[binding.ContentID]; exists {
			return fmt.Errorf("content ID %q is bound to digests %s and %s", binding.ContentID, previous, key)
		}
		contentIDs[binding.ContentID] = key
	}
	for key, tombstone := range state.Tombstones {
		if strings.TrimSpace(key) == "" || key != tombstone.ContentID || strings.TrimSpace(tombstone.ContentID) == "" {
			return errors.New("tombstone key and content ID must be identical and nonblank")
		}
		if tombstone.Digest != "" && !validDigest(tombstone.Digest) {
			return fmt.Errorf("tombstone %q has invalid digest", key)
		}
		if tombstone.RecordedAt.IsZero() {
			return fmt.Errorf("tombstone %q has no recorded time", key)
		}
	}
	if err := validateStateEvidence(state); err != nil {
		return err
	}
	if state.Pending != nil {
		if err := validatePending(*state.Pending); err != nil {
			return err
		}
	}
	if state.LastCollectionGen != "" && !validDigest(state.LastCollectionGen) {
		return errors.New("last collection generation is invalid")
	}
	return nil
}

func validateStateEvidence(state State) error {
	if state.Capacity.Known && (state.Capacity.Maximum < 0 || state.Capacity.ObservedAt.IsZero()) {
		return errors.New("known capacity evidence is invalid")
	}
	if state.Capacity.Probe && !state.Capacity.Known {
		return errors.New("capacity probe requires known evidence")
	}
	if state.LegacyMigrationPending &&
		(len(state.Bindings) != 0 || state.LastCompleteCycle != "" || state.LastCollectionGen != "") {
		return errors.New("pending legacy migration cannot coexist with completed reconciliation state")
	}
	return nil
}

func sameStableTV(left, right TVIdentity) bool {
	return left.Address == right.Address && left.Model == right.Model
}

func validateBinding(key string, binding Binding) error {
	if !validDigest(key) || binding.Digest != key {
		return fmt.Errorf("binding key %q does not match a full digest", key)
	}
	if strings.TrimSpace(binding.ContentID) == "" || binding.ContentID != strings.TrimSpace(binding.ContentID) {
		return fmt.Errorf("binding %s has invalid content ID", key)
	}
	if filepath.Base(binding.Name) != binding.Name || strings.TrimSpace(binding.Name) == "" {
		return fmt.Errorf("binding %s has invalid name", key)
	}
	if binding.Size < 0 {
		return fmt.Errorf("binding %s has invalid size", key)
	}
	if !validDigest(binding.CollectionGeneration) || binding.ConfirmedAt.IsZero() {
		return fmt.Errorf("binding %s has invalid confirmation evidence", key)
	}
	return nil
}

//nolint:gocyclo,nestif // Phase compatibility is an explicit state-machine matrix.
func validatePending(pending Pending) error {
	if strings.TrimSpace(pending.OperationID) == "" || strings.TrimSpace(pending.CycleID) == "" {
		return errors.New("pending operation and cycle IDs are required")
	}
	if !validPendingFingerprints(pending) {
		return errors.New("pending operation fingerprints are invalid")
	}
	if pending.Phase < PhasePrepared || pending.Phase > PhaseApplied {
		return fmt.Errorf("pending operation has invalid phase %d", pending.Phase)
	}
	if err := validateIntent(pending.Command); err != nil {
		return fmt.Errorf("pending operation command: %w", err)
	}
	if pending.Phase == PhaseApplied {
		if pending.Receipt == nil || pending.Receipt.Outcome != samsung.OutcomeApplied ||
			strings.TrimSpace(pending.Receipt.CommandID) == "" || pending.Receipt.CompletedAt.IsZero() {
			return errors.New("applied pending operation requires a positive receipt")
		}
	} else if pending.Receipt != nil {
		if pending.Phase != PhaseOutcomeUnknown || pending.Receipt.Outcome != samsung.OutcomeUnknown {
			return errors.New("receipt is incompatible with pending phase")
		}
	}
	return nil
}

func validPendingFingerprints(pending Pending) bool {
	if !validDigest(pending.CollectionGen) || pending.PolicyFingerprint == ([sha256.Size]byte{}) {
		return false
	}
	return pending.Command.Kind == CommandWake || pending.InventoryBefore.Digest != ([sha256.Size]byte{})
}

//nolint:gocognit,gocyclo // Each sealed intent has distinct fail-closed validation.
func validateIntent(intent CommandIntent) error {
	switch intent.Kind {
	case CommandUpload:
		if !validDigest(intent.Digest) || filepath.IsAbs(intent.Name) || filepath.Base(intent.Name) != intent.Name ||
			strings.TrimSpace(intent.Name) == "" || !filepath.IsAbs(intent.Path) ||
			(intent.FileType != collection.FileTypeJPEG && intent.FileType != collection.FileTypePNG) ||
			intent.Size <= 0 || strings.TrimSpace(intent.Matte) == "" || len(intent.Matte) > 128 {
			return errors.New("upload intent has invalid digest, name, path, or file type")
		}
	case CommandDeleteOwned:
		if !validDigest(intent.Digest) || strings.TrimSpace(intent.ContentID) == "" {
			return errors.New("owned deletion requires digest and content ID")
		}
	case CommandDeleteUnknown:
		if strings.TrimSpace(intent.ContentID) == "" || !intent.RemoveUnknownApproved {
			return errors.New("unknown deletion requires content ID and explicit approval")
		}
	case CommandSelect:
		if strings.TrimSpace(intent.ContentID) == "" {
			return errors.New("selection requires content ID")
		}
	case CommandSlideshow:
		if intent.PreviousSlideshow == nil || intent.DesiredSlideshow == nil ||
			!intent.PreviousSlideshow.Valid() || !intent.DesiredSlideshow.Valid() {
			return errors.New("slideshow intent requires known valid previous and desired settings")
		}
	case CommandBrightness:
		if intent.PreviousValue == nil || intent.DesiredValue == nil {
			return errors.New("setting intent requires known previous and desired values")
		}
	case CommandPowerOff, CommandWake:
		// No command-specific payload.
	default:
		return fmt.Errorf("unknown command kind %q", intent.Kind)
	}
	return nil
}

//nolint:gocyclo // Snapshot validation keeps all mutation-authority checks together.
func validateSnapshot(snapshot collection.Snapshot) error {
	if !validDigest(snapshot.Generation) {
		return errors.New("collection snapshot generation is invalid")
	}
	items := slices.Clone(snapshot.Items)
	slices.SortFunc(items, func(left, right collection.Item) int { return strings.Compare(left.Name, right.Name) })
	names := make(map[string]struct{}, len(items))
	digests := make(map[string]struct{}, len(items))
	for _, item := range items {
		digest := hex.EncodeToString(item.Digest[:])
		if _, exists := names[item.Name]; exists {
			return fmt.Errorf("collection snapshot repeats name %q", item.Name)
		}
		if _, exists := digests[digest]; exists {
			return fmt.Errorf("collection snapshot repeats digest %s", digest)
		}
		if filepath.Base(item.Name) != item.Name || !filepath.IsAbs(item.Path) || item.Size <= 0 || item.Width <= 0 || item.Height <= 0 ||
			(item.Type != collection.FileTypeJPEG && item.Type != collection.FileTypePNG) || item.Digest == ([sha256.Size]byte{}) {
			return fmt.Errorf("collection snapshot item %q is invalid", item.Name)
		}
		names[item.Name] = struct{}{}
		digests[digest] = struct{}{}
	}
	if collection.SnapshotGeneration(items) != snapshot.Generation {
		return errors.New("collection snapshot generation does not match its items")
	}
	return nil
}

func validateObservation(observation samsung.Observation) error {
	if _, err := identityFromObservation(observation); err != nil {
		return err
	}
	if !observation.Inventory.Known {
		return errors.New("TV inventory is unknown")
	}
	if strings.TrimSpace(observation.Inventory.CategoryID) == "" || observation.Inventory.ObservedAt.IsZero() {
		return errors.New("TV inventory evidence is incomplete")
	}
	ids := slices.Clone(observation.Inventory.ContentIDs)
	for index, id := range ids {
		if id == "" || id != strings.TrimSpace(id) || (index > 0 && ids[index-1] >= id) {
			return errors.New("TV inventory IDs must be nonblank, sorted, and unique")
		}
	}
	canonical, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("encode TV inventory fingerprint: %w", err)
	}
	if sha256.Sum256(canonical) != observation.Inventory.Fingerprint {
		return errors.New("TV inventory fingerprint does not match its content IDs")
	}
	return nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
