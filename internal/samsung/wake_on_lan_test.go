package samsung

import (
	"bytes"
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestWakeOnLANValidationHonorsCancellation(t *testing.T) {
	targets := []string{"127.0.0.1:9"}
	if err := sendWakeOnLAN(context.Background(), net.HardwareAddr{1, 2, 3}, targets, time.Second); err == nil {
		t.Fatal("short MAC address accepted")
	}
	if err := sendWakeOnLAN(context.Background(), net.HardwareAddr{0, 1, 2, 3, 4, 5}, nil, time.Second); err == nil {
		t.Fatal("Wake-on-LAN without targets accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := sendWakeOnLAN(canceled, net.HardwareAddr{0, 1, 2, 3, 4, 5}, targets, time.Second)
	if err == nil {
		t.Fatal("Wake-on-LAN ignored canceled context")
	}
}

func TestWakeTargetsIncludeDirectedBroadcastAndBothPorts(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		override string
		want     []string
	}{
		{
			name: "IPv4 TV", host: "192.168.4.37",
			want: []string{"255.255.255.255:9", "255.255.255.255:7", "192.168.4.255:9", "192.168.4.255:7"},
		},
		{
			name: "operator override", host: "192.168.4.37", override: "10.0.0.255",
			want: []string{"10.0.0.255:9", "10.0.0.255:7"},
		},
		{
			name: "IPv6 TV", host: "fe80::1",
			want: []string{"255.255.255.255:9", "255.255.255.255:7"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := wakeTargets(test.host, test.override); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("wakeTargets() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWakeOnLANSendsMagicPacketWhenSomeTargetsFail(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for magic packet: %v", err)
	}
	defer func() { _ = listener.Close() }()
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	targets := []string{"invalid host:9", listener.LocalAddr().String()}
	if err := sendWakeOnLAN(context.Background(), mac, targets, time.Second); err != nil {
		t.Fatalf("sendWakeOnLAN() error = %v", err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	packet := make([]byte, 256)
	length, _, err := listener.ReadFrom(packet)
	if err != nil {
		t.Fatalf("read magic packet: %v", err)
	}
	want := append(bytes.Repeat([]byte{0xff}, 6), bytes.Repeat(mac, 16)...)
	if !bytes.Equal(packet[:length], want) {
		t.Fatalf("magic packet = %x, want %x", packet[:length], want)
	}
	if err := sendWakeOnLAN(context.Background(), mac, []string{"invalid host:9"}, time.Second); err == nil {
		t.Fatal("Wake-on-LAN reported success when every target failed")
	}
}
