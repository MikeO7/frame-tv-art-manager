package samsung

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// FetchDeviceInfo retrieves device metadata from the TV's REST API.
//
// Endpoint: GET https://<host>:8002/api/v2/
// The TV uses a self-signed certificate, so TLS verification is skipped.
func FetchDeviceInfo(ctx context.Context, host string, port int, timeout time.Duration) (*DeviceInfo, error) {
	url := fmt.Sprintf("https://%s:%d/api/v2/", host, port)

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Samsung self-signed cert
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var envelope DeviceInfoResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse device info: %w", err)
	}

	return &envelope.Device, nil
}
