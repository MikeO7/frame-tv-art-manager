package reconcile

import (
	"cmp"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

const (
	maximumRotation = 365 * 24 * time.Hour
	// maximumShortfalls stops rotation deletions after repeated storage-full
	// rejections below the recorded capacity. A successful upload resets it.
	maximumShortfalls = 3
)

// waitingArtwork is desired artwork that is not bound on the TV.
type waitingArtwork struct {
	item      collection.Item
	digest    string
	lastShown time.Time
}

type rotationSwap struct {
	evict  Binding
	upload waitingArtwork
}

type capacityInput struct {
	waiting   []waitingArtwork
	owned     []Binding
	state     State
	inventory int
	policy    Policy
	now       time.Time
}

type capacityPlan struct {
	fills    []waitingArtwork
	swaps    []rotationSwap
	limited  bool
	rotation RotationStatus
}

// prioritizeWaiting orders waiting artwork so artwork that has never been
// shown comes first, followed by artwork that has been off the TV longest.
// Digest order breaks ties deterministically.
func prioritizeWaiting(waiting []waitingArtwork) {
	slices.SortFunc(waiting, func(left, right waitingArtwork) int {
		leftShown, rightShown := !left.lastShown.IsZero(), !right.lastShown.IsZero()
		if leftShown != rightShown {
			if leftShown {
				return 1
			}
			return -1
		}
		if order := left.lastShown.Compare(right.lastShown); order != 0 {
			return order
		}
		return strings.Compare(left.digest, right.digest)
	})
}

// planCapacity admits waiting artwork into free TV slots and, when rotation
// is active, pairs owned artwork that has been displayed for the rotation
// interval with the waiting artwork that has been away longest. Each pair is
// emitted as a deletion followed by its replacement upload, so the TV is never
// more than one item below its known capacity.
func planCapacity(input capacityInput) capacityPlan {
	prioritizeWaiting(input.waiting)
	capacity := input.state.Capacity
	if !capacity.Known {
		return capacityPlan{fills: input.waiting}
	}
	free := min(max(capacity.Maximum-input.inventory, 0), len(input.waiting))
	result := capacityPlan{fills: input.waiting[:free]}
	waiting := input.waiting[free:]
	if len(waiting) == 0 {
		return result
	}
	if !rotationActive(input) {
		result.limited = true
		return result
	}
	interval := input.policy.Rotation
	eligible := 0
	for eligible < len(waiting) && rotationDue(waiting[eligible].lastShown, interval, input.now) {
		eligible++
	}
	owned := slices.Clone(input.owned)
	slices.SortFunc(owned, func(left, right Binding) int {
		return cmp.Or(left.ConfirmedAt.Compare(right.ConfirmedAt), strings.Compare(left.Digest, right.Digest))
	})
	evictable := 0
	for evictable < len(owned) && !input.now.Before(owned[evictable].ConfirmedAt.Add(interval)) {
		evictable++
	}
	swaps := min(eligible, evictable)
	for index := range swaps {
		result.swaps = append(result.swaps, rotationSwap{evict: owned[index], upload: waiting[index]})
	}
	result.rotation = RotationStatus{
		Active: true,
		// Each swap returns its evicted item to the waiting set.
		Waiting: len(waiting),
		NextAt:  nextRotation(owned[swaps:], waiting[swaps:], swaps > 0, interval, input.now),
	}
	return result
}

// rotationActive reports whether swaps may replace owned artwork. Rotation
// keeps at least one owned item bound at every step unless policy permits an
// empty managed collection, and it pauses after repeated shortfalls.
func rotationActive(input capacityInput) bool {
	minimumOwned := 2
	if input.policy.AllowEmpty {
		minimumOwned = 1
	}
	capacity := input.state.Capacity
	return input.policy.Rotation > 0 && !capacity.Probe && capacity.Shortfalls < maximumShortfalls &&
		len(input.owned) >= minimumOwned
}

func rotationDue(lastShown time.Time, interval time.Duration, now time.Time) bool {
	return lastShown.IsZero() || !now.Before(lastShown.Add(interval))
}

// nextRotation returns the earliest time at which both an owned item and a
// waiting item become eligible for a swap. Planned swaps contribute a newly
// uploaded owned item and a newly removed waiting item, both due one interval
// from now.
func nextRotation(owned []Binding, waiting []waitingArtwork, swapped bool, interval time.Duration, now time.Time) time.Time {
	var ownedDue, waitingDue time.Time
	if swapped {
		ownedDue, waitingDue = now.Add(interval), now.Add(interval)
	}
	if len(owned) > 0 {
		ownedDue = earliest(ownedDue, owned[0].ConfirmedAt.Add(interval))
	}
	if len(waiting) > 0 {
		due := now
		if !waiting[0].lastShown.IsZero() {
			due = waiting[0].lastShown.Add(interval)
		}
		waitingDue = earliest(waitingDue, due)
	}
	if ownedDue.IsZero() || waitingDue.IsZero() {
		return time.Time{}
	}
	next := ownedDue
	if waitingDue.After(next) {
		next = waitingDue
	}
	if next.Before(now) {
		return now
	}
	return next
}

func earliest(current, candidate time.Time) time.Time {
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}

// recordShown notes that desired artwork left the TV at the given time.
func recordShown(state *State, digest string, at time.Time) {
	if state.LastShown == nil {
		state.LastShown = make(map[string]time.Time)
	}
	state.LastShown[digest] = at.UTC()
}

// retainShownHistory keeps rotation history only for artwork that is still
// in the Artwork Collection, so the history never outgrows the collection.
func retainShownHistory(state *State, desired map[string]struct{}) {
	for digest := range state.LastShown {
		if _, wanted := desired[digest]; !wanted {
			delete(state.LastShown, digest)
		}
	}
	if len(state.LastShown) == 0 {
		state.LastShown = nil
	}
}

func snapshotDigests(snapshot collection.Snapshot) map[string]struct{} {
	digests := make(map[string]struct{}, len(snapshot.Items))
	for _, item := range snapshot.Items {
		digests[hex.EncodeToString(item.Digest[:])] = struct{}{}
	}
	return digests
}

// storageFullCapacity records a storage-full rejection. A rejection while the
// TV held fewer items than the recorded maximum counts as a shortfall.
func storageFullCapacity(previous CapacityEvidence, inventory int, now time.Time) CapacityEvidence {
	shortfalls := previous.Shortfalls
	if previous.Known && !previous.Probe && inventory < previous.Maximum {
		shortfalls++
	}
	return CapacityEvidence{Known: true, Maximum: inventory, ObservedAt: now, Shortfalls: shortfalls}
}
