package reconcile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func validatePolicy(policy Policy) error {
	if policy.Power > PowerOn || policy.Slideshow.Mode > PolicySet || policy.Brightness.Mode > PolicySet {
		return errors.New("reconciliation policy contains an invalid mode")
	}
	if err := validateSlideshowPolicy(policy.Slideshow); err != nil {
		return err
	}
	if policy.Brightness.Mode == PolicySet && (policy.Brightness.Value < 0 || policy.Brightness.Value > 100) {
		return errors.New("brightness policy value must be between 0 and 100")
	}
	if policy.DefaultMatte != "" &&
		(policy.DefaultMatte != strings.TrimSpace(policy.DefaultMatte) || len(policy.DefaultMatte) > 128) {
		return errors.New("default matte must be normalized and no longer than 128 characters")
	}
	if _, err := policy.MatteOverrides.entries(); err != nil {
		return fmt.Errorf("invalid matte override policy: %w", err)
	}
	return nil
}

func validateSlideshowPolicy(policy SlideshowPolicy) error {
	switch policy.Mode {
	case PolicyPreserve:
		return nil
	case PolicyDisable:
		if policy.Setting.Interval != 0 {
			return errors.New("disabled slideshow policy interval must be zero")
		}
		if policy.Setting.Kind != "" && !policy.Setting.Valid() {
			return errors.New("disabled slideshow policy kind is invalid")
		}
		return nil
	case PolicySet:
		if !policy.Setting.Valid() || policy.Setting.Interval <= 0 {
			return errors.New("slideshow policy setting must have a positive interval and valid kind")
		}
		return nil
	default:
		return errors.New("reconciliation policy contains an invalid slideshow mode")
	}
}

func fingerprintPolicy(policy Policy) ([sha256.Size]byte, error) {
	policy = normalizePolicy(policy)
	if err := validatePolicy(policy); err != nil {
		return [sha256.Size]byte{}, err
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode reconciliation policy: %w", err)
	}
	return sha256.Sum256(data), nil
}

func normalizePolicy(policy Policy) Policy {
	if policy.DefaultMatte == "" {
		policy.DefaultMatte = defaultMatte
	}
	return policy
}
