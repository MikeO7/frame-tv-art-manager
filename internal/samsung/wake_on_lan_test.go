package samsung

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestWakeOnLANValidationHonorsCancellation(t *testing.T) {
	if err := sendWakeOnLAN(context.Background(), net.HardwareAddr{1, 2, 3}, time.Second); err == nil {
		t.Fatal("short MAC address accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := sendWakeOnLAN(canceled, net.HardwareAddr{0, 1, 2, 3, 4, 5}, time.Second)
	if err == nil {
		t.Fatal("Wake-on-LAN ignored canceled context")
	}
}
