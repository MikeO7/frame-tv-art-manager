package samsung

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

func (c *Client) fetchDeviceInfo(ctx context.Context, port int) (*DeviceInfo, error) {
	url := fmt.Sprintf("https://%s:%d/api/v2/", c.IP, port)

	client := &http.Client{
		Timeout: c.opts.APITimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: c.opts.SkipTLSVerify, //nolint:gosec // configurable TLS verification
			},
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
	defer func() { _ = resp.Body.Close() }()

	// Prevent DoS / resource exhaustion by enforcing a 1MB maximum read size
	maxBytes := int64(1 * 1024 * 1024)
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var envelope deviceInfoResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse device info: %w", err)
	}

	return &envelope.Device, nil
}

var macSeparators = regexp.MustCompile(`[^a-fA-F0-9]`)

func (c *Client) sendWOL(ctx context.Context, macAddr string) error {
	if macAddr == "" {
		return nil
	}

	// Strip separators and validate length.
	clean := macSeparators.ReplaceAllString(macAddr, "")
	clean = strings.ToLower(clean)
	if len(clean) != 12 {
		return fmt.Errorf("invalid MAC address %q: expected 12 hex chars, got %d", macAddr, len(clean))
	}

	// Parse hex bytes.
	mac := make([]byte, 6)
	for i := 0; i < 6; i++ {
		_, err := fmt.Sscanf(clean[i*2:i*2+2], "%02x", &mac[i])
		if err != nil {
			return fmt.Errorf("invalid MAC address %q: %w", macAddr, err)
		}
	}

	// Build magic packet: 6 bytes of 0xFF followed by MAC repeated 16 times.
	packet := make([]byte, 6+16*6)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 0; i < 16; i++ {
		copy(packet[6+i*6:], mac)
	}

	// Send to broadcast address.
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", "255.255.255.255:9")
	if err != nil {
		return fmt.Errorf("dial broadcast: %w", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = conn.Write(packet)
	if err != nil {
		return fmt.Errorf("send magic packet: %w", err)
	}

	return nil
}

func (c *Client) turnOffTV(ctx context.Context, port int) error {
	conn := newConnection(
		c.IP, port, "samsung.remote.control",
		c.opts.ClientName, c.tokenFilePath(),
		c.opts.ConnectionTimeout, c.opts.SkipTLSVerify, c.logger,
	)

	if err := conn.Open(ctx); err != nil {
		return fmt.Errorf("open remote control connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Send KEY_POWER press.
	press := map[string]any{
		keyMethod: methodRemoteControl,
		keyParams: map[string]any{
			"Cmd":          "Press",
			"DataOfCmd":    "KEY_POWER",
			"Option":       stringFalse,
			"TypeOfRemote": "SendRemoteKey",
		},
	}

	pressPayload, err := json.Marshal(press)
	if err != nil {
		return fmt.Errorf("marshal press command: %w", err)
	}

	if err := conn.Send(pressPayload); err != nil {
		return fmt.Errorf("send press: %w", err)
	}

	// Hold for 3 seconds.
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Send KEY_POWER release.
	release := map[string]any{
		keyMethod: methodRemoteControl,
		keyParams: map[string]any{
			"Cmd":          "Release",
			"DataOfCmd":    "KEY_POWER",
			"Option":       stringFalse,
			"TypeOfRemote": "SendRemoteKey",
		},
	}

	releasePayload, err := json.Marshal(release)
	if err != nil {
		return fmt.Errorf("marshal release command: %w", err)
	}

	if err := conn.Send(releasePayload); err != nil {
		return fmt.Errorf("send release: %w", err)
	}

	return nil
}
