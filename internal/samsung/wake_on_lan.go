package samsung

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

func sendWakeOnLAN(ctx context.Context, mac net.HardwareAddr, timeout time.Duration) error {
	if len(mac) != 6 {
		return errors.New("Wake-on-LAN requires a six-byte MAC address")
	}
	packet := make([]byte, 6+16*len(mac))
	for index := range 6 {
		packet[index] = 0xff
	}
	for index := range 16 {
		copy(packet[6+index*len(mac):], mac)
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "udp", fmt.Sprintf("255.255.255.255:%d", wolBroadcastPort))
	if err != nil {
		return fmt.Errorf("dial Wake-on-LAN broadcast: %w", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write(packet); err != nil {
		return fmt.Errorf("send Wake-on-LAN packet: %w", err)
	}
	return nil
}
