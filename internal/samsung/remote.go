package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// TurnOffTV connects to the TV's remote control WebSocket endpoint and
// sends a KEY_POWER hold for 3 seconds, which powers off a Frame TV.
//
// A single KEY_POWER press only toggles art mode; holding for 3 seconds
// is required for a full power-off.
//
// This uses a SEPARATE WebSocket connection to the "samsung.remote.control"
// endpoint (not the art app endpoint) because the art API cannot send
// remote key commands.
func TurnOffTV(ctx context.Context, host string, port int, clientName, tokenFile string, timeout time.Duration, logger *slog.Logger) error {
	conn := NewConnection(host, port, "samsung.remote.control", clientName, tokenFile, timeout, logger)

	if err := conn.Open(ctx); err != nil {
		return fmt.Errorf("open remote control connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Send KEY_POWER press.
	press := map[string]any{
		keyMethod: "ms.remote.control",
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
		keyMethod: "ms.remote.control",
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

// EnsureToken establishes a connection to the "samsung.remote.control" endpoint
// specifically to trigger the TV's authorization prompt and save a persistent
// token if one doesn't exist.
func EnsureToken(ctx context.Context, host string, port int, clientName, tokenFile string, timeout time.Duration, logger *slog.Logger) error {
	conn := NewConnection(host, port, "samsung.remote.control", clientName, tokenFile, timeout, logger)

	// NewConnection.Open() handles the handshake and automatically saves
	// the token to tokenFile if it's received in the ms.channel.connect event.
	if err := conn.Open(ctx); err != nil {
		return fmt.Errorf("remote handshake failed: %w", err)
	}
	defer func() { _ = conn.Close() }()

	return nil
}
