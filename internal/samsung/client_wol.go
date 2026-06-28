package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// wakeTV sends a Wake-on-LAN magic packet to the TV to wake it up if it's sleeping.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
func (c *Client) wakeTV(ctx context.Context) {
	if c.opts.TVMAC == "" {
		return
	}
	c.logger.Info("sending Wake-on-LAN", "mac", c.opts.TVMAC)
	if err := c.sendWOL(ctx, c.opts.TVMAC); err != nil {
		c.logger.Warn("WoL failed", "error", err)
	} else {
		time.Sleep(2 * time.Second)
	}
}

var macSeparators = regexp.MustCompile(`[^a-fA-F0-9]`)

// sendWOL broadcasts a Wake-on-LAN magic packet to wake up the TV on the local network.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the network request.
//   - macAddr: The MAC address string (e.g., "AA:BB:CC:DD:EE:FF") of the TV.
//
// Returns:
//   - error: Any formatting error or network failure encountered while broadcasting.
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
	conn, err := dialer.DialContext(ctx, "udp", fmt.Sprintf("255.255.255.255:%d", wolBroadcastPort))
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

// TurnOff powers off the TV via the remote control API.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the network requests.
//
// Returns:
//   - error: Any network or protocol error encountered during the power sequence.
func (c *Client) TurnOff(ctx context.Context) error {
	return c.turnOffTV(ctx, portArtWSS)
}

func (c *Client) turnOffTV(ctx context.Context, port int) error {
	conn := newConnection(c.remoteControlConfig(port, c.tokenFilePath()))

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

	if err := conn.Send(ctx, pressPayload); err != nil {
		return fmt.Errorf("send press: %w", err)
	}

	// Hold KEY_POWER before releasing to trigger the power toggle.
	hold := c.powerHold
	if hold <= 0 {
		hold = powerKeyHold
	}
	select {
	case <-time.After(hold):
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

	if err := conn.Send(ctx, releasePayload); err != nil {
		return fmt.Errorf("send release: %w", err)
	}

	return nil
}
