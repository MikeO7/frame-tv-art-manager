package samsung

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// CheckArtModeGate probes the TV's art service endpoint to determine if
// it is currently in Art Mode. This is the "Silent REST Gate" — a
// non-intrusive check that does NOT trigger Allow/Deny popups.
//
// Endpoint: GET http://<host>:8001/ms/art  (plain HTTP, NOT HTTPS)
//
// Responses:
//   - 200 OK:  TV is in Art Mode → safe to connect via WSS
//   - 404:     TV is running an app (Netflix, YouTube, etc.) → skip
//   - timeout: TV is off or busy → skip
//
// NOTE: Not all firmware versions support this endpoint. On some 2024
// models, /ms/art returns 404 regardless of state. This gate should be
// treated as opt-in via configuration.
func CheckArtModeGate(ctx context.Context, host string, timeout time.Duration) (bool, error) {
	url := fmt.Sprintf("http://%s:8001/ms/art", host)

	client := &http.Client{Timeout: timeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, nil // not a fatal error, just can't check
	}

	resp, err := client.Do(req)
	if err != nil {
		// Timeout or connection refused — TV is off or busy.
		return false, nil
	}
	defer resp.Body.Close()

	// Only 200 OK means the TV is definitively in Art Mode.
	return resp.StatusCode == http.StatusOK, nil
}
