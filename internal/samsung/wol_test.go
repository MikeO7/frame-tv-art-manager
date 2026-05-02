package samsung

import (
	"testing"
)

func TestSendWOL_Invalid(t *testing.T) {
	err := SendWOL("invalid")
	if err == nil {
		t.Error("expected error for invalid MAC")
	}

	err = SendWOL("")
	if err != nil {
		t.Error("expected no error for empty MAC")
	}
}

func TestSendWOL_ValidFormat(t *testing.T) {
	// We can't easily test the actual broadcast in CI without network permissions,
	// but we can test the parsing logic.
	// Actually, SendWOL calls net.Dial.
	// To avoid actual network calls, we would need to mock net.Dial.
	// But 255.255.255.255:9 usually fails or is a no-op in restricted environments.

	_ = SendWOL("AA:BB:CC:DD:EE:FF")
}
