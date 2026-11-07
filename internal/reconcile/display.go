package reconcile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const (
	// maximumShowInterval bounds how long one picture stays on screen.
	maximumShowInterval = 24 * time.Hour
	// showEarlyFraction lets a picture change up to a tenth of the interval
	// early, so a timer that fires a moment before the deadline does not wait
	// a whole extra interval.
	showEarlyFraction = 10
)

// DisplayState is the manager's own picture rotation on one TV.
type DisplayState struct {
	// Digest and At are the picture the manager last put on screen and when.
	Digest string    `json:"digest"`
	At     time.Time `json:"at"`
	// Round counts passes through the collection. Picked records the round in
	// which each picture was last shown, so a shuffle shows every picture once
	// before any repeats.
	Round  int            `json:"round"`
	Picked map[string]int `json:"picked,omitempty"`
}

type showCandidate struct {
	digest    string
	name      string
	contentID string
}

// planShow returns the select command for the next picture once the current
// one has been on screen for the show interval. It never selects without a
// verified selection capability, and never re-selects what is already shown.
func planShow(
	desired map[string]collection.Item,
	state State,
	inventory map[string]struct{},
	observation samsung.Observation,
	policy ShowPolicy,
) (CommandIntent, bool) {
	if policy.Interval <= 0 || observation.Capabilities.ImageSelection != samsung.SupportSupported {
		return CommandIntent{}, false
	}
	display := state.Display
	if display != nil && observation.ObservedAt.Before(display.At.Add(policy.Interval-policy.Interval/showEarlyFraction)) {
		return CommandIntent{}, false
	}
	shown := showCandidates(desired, state, inventory)
	round, last := 1, ""
	var picked map[string]int
	if display != nil {
		round, last, picked = max(display.Round, 1), display.Digest, display.Picked
	}
	onScreen := strings.TrimSpace(observation.CurrentArtwork)
	pool := func(round int) []showCandidate {
		return slices.DeleteFunc(slices.Clone(shown), func(candidate showCandidate) bool {
			return candidate.digest == last || candidate.contentID == onScreen || picked[candidate.digest] == round
		})
	}
	candidates := pool(round)
	if len(candidates) == 0 {
		round++
		candidates = pool(round)
	}
	if len(candidates) == 0 {
		return CommandIntent{}, false
	}
	var next showCandidate
	if policy.Order == ShowSequential {
		next = nextInNameOrder(candidates, shown, last)
	} else {
		next = nextInShuffle(candidates, round)
	}
	return CommandIntent{Kind: CommandSelect, Digest: next.digest, ContentID: next.contentID, Round: round}, true
}

// showCandidates lists the desired pictures bound on the TV in name order.
func showCandidates(desired map[string]collection.Item, state State, inventory map[string]struct{}) []showCandidate {
	shown := make([]showCandidate, 0, len(desired))
	for digest, item := range desired {
		binding, bound := state.Bindings[digest]
		if _, present := inventory[binding.ContentID]; bound && present {
			shown = append(shown, showCandidate{digest: digest, name: item.Name, contentID: binding.ContentID})
		}
	}
	slices.SortFunc(shown, func(left, right showCandidate) int {
		return strings.Compare(left.sortKey(), right.sortKey())
	})
	return shown
}

func (candidate showCandidate) sortKey() string {
	return candidate.name + "\x00" + candidate.digest
}

// nextInNameOrder returns the first candidate after the last shown picture,
// wrapping to the start. Candidates are in name order.
func nextInNameOrder(candidates, shown []showCandidate, last string) showCandidate {
	lastKey := ""
	for _, candidate := range shown {
		if candidate.digest == last {
			lastKey = candidate.sortKey()
		}
	}
	for _, candidate := range candidates {
		if candidate.sortKey() > lastKey {
			return candidate
		}
	}
	return candidates[0]
}

// nextInShuffle orders candidates by a hash keyed with the round, so each
// round has its own random order while the planner stays deterministic.
func nextInShuffle(candidates []showCandidate, round int) showCandidate {
	key := func(candidate showCandidate) [sha256.Size]byte {
		return sha256.Sum256([]byte(candidate.digest + ":" + strconv.Itoa(round)))
	}
	next, nextKey := candidates[0], key(candidates[0])
	for _, candidate := range candidates[1:] {
		if candidateKey := key(candidate); bytes.Compare(candidateKey[:], nextKey[:]) < 0 {
			next, nextKey = candidate, candidateKey
		}
	}
	return next
}

// recordDisplay notes a verified picture change.
func recordDisplay(state *State, intent CommandIntent, at time.Time) {
	if intent.Digest == "" || intent.Round <= 0 {
		return
	}
	display := DisplayState{Digest: intent.Digest, At: at.UTC(), Round: intent.Round, Picked: map[string]int{}}
	if state.Display != nil {
		maps.Copy(display.Picked, state.Display.Picked)
	}
	display.Picked[intent.Digest] = intent.Round
	state.Display = &display
}

// retainDisplayHistory keeps shuffle history only for pictures that are still
// in the Artwork Collection.
func retainDisplayHistory(state *State, desired map[string]struct{}) {
	if state.Display == nil {
		return
	}
	picked := make(map[string]int, len(state.Display.Picked))
	for digest, round := range state.Display.Picked {
		if _, wanted := desired[digest]; wanted {
			picked[digest] = round
		}
	}
	if len(picked) == 0 {
		picked = nil
	}
	display := *state.Display
	display.Picked = picked
	state.Display = &display
}

func cloneDisplay(display *DisplayState) *DisplayState {
	if display == nil {
		return nil
	}
	clone := *display
	clone.Picked = maps.Clone(display.Picked)
	return &clone
}

func validateDisplay(display *DisplayState) error {
	if display == nil {
		return nil
	}
	if !validDigest(display.Digest) || display.At.IsZero() || display.Round <= 0 {
		return errors.New("display state is invalid")
	}
	for digest, round := range display.Picked {
		if !validDigest(digest) || round <= 0 || round > display.Round {
			return fmt.Errorf("display history entry %q is invalid", digest)
		}
	}
	return nil
}

func validateShowPolicy(policy ShowPolicy) error {
	if policy.Interval < 0 || policy.Interval > maximumShowInterval {
		return fmt.Errorf("show interval must be between 0 and %s", maximumShowInterval)
	}
	if policy.Order > ShowSequential {
		return errors.New("show order is invalid")
	}
	return nil
}
