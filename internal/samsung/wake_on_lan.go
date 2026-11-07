package samsung

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// wakeTargets lists the UDP destinations for a magic packet. An explicit
// broadcast address replaces the defaults; otherwise the packet goes to the
// limited broadcast address and, for an IPv4 TV, the directed broadcast of
// its /24, which routers and Docker bridges forward more reliably.
func wakeTargets(tvHost, override string) []string {
	addresses := []string{"255.255.255.255"}
	if override != "" {
		addresses = []string{override}
	} else if ip := net.ParseIP(tvHost).To4(); ip != nil {
		addresses = append(addresses, net.IPv4(ip[0], ip[1], ip[2], 255).String())
	}
	targets := make([]string, 0, len(addresses)*2)
	for _, address := range addresses {
		for _, port := range []int{wolBroadcastPort, wolEchoPort} {
			targets = append(targets, net.JoinHostPort(address, strconv.Itoa(port)))
		}
	}
	return targets
}

// sendWakeOnLAN sends the magic packet to every target. It succeeds when at
// least one packet left the host, because unreachable broadcast routes are
// normal on multi-homed or containerized hosts.
func sendWakeOnLAN(ctx context.Context, mac net.HardwareAddr, targets []string, timeout time.Duration) error {
	if len(mac) != 6 {
		return errors.New("Wake-on-LAN requires a six-byte MAC address")
	}
	if len(targets) == 0 {
		return errors.New("Wake-on-LAN requires at least one target")
	}
	packet := make([]byte, 6+16*len(mac))
	for index := range 6 {
		packet[index] = 0xff
	}
	for index := range 16 {
		copy(packet[6+index*len(mac):], mac)
	}
	var failures []error
	for _, target := range targets {
		if err := sendWakePacket(ctx, packet, target, timeout); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == len(targets) {
		return errors.Join(failures...)
	}
	return nil
}

func sendWakePacket(ctx context.Context, packet []byte, target string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "udp", target)
	if err != nil {
		return fmt.Errorf("dial Wake-on-LAN target %s: %w", target, err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write(packet); err != nil {
		return fmt.Errorf("send Wake-on-LAN packet to %s: %w", target, err)
	}
	return nil
}
