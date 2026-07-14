package reconcile

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type randomIDs struct{}

func (randomIDs) NewID() (string, error) {
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate reconciliation operation ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
