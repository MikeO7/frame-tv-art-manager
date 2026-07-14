package samsung

import "testing"

// ProtocolTVFixture is the test-only local Samsung protocol server used by
// production-boundary integration tests.
type ProtocolTVFixture = protocolTVFixture

// NewProtocolTVFixture starts the shared local Samsung protocol fixture.
func NewProtocolTVFixture(t *testing.T) *ProtocolTVFixture {
	t.Helper()
	return newProtocolTVFixture(t)
}

// Address returns the fixture's TLS WebSocket and device-info address.
func (f *protocolTVFixture) Address(t *testing.T) string {
	t.Helper()
	return f.address(t)
}

// DeleteMutations returns successful delete commands observed on the wire.
func (f *protocolTVFixture) DeleteMutations() []string {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return append([]string(nil), f.deleteMutations...)
}

// Inventory returns the fixture's current user-art content IDs.
func (f *protocolTVFixture) Inventory() []string {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return append([]string(nil), f.inventory...)
}

// SlideshowSetting returns the fixture's current typed slideshow state.
func (f *protocolTVFixture) SlideshowSetting() SlideshowSetting {
	return f.slideshowSetting()
}

// SlideshowMutations returns typed slideshow writes observed on the wire.
func (f *protocolTVFixture) SlideshowMutations() []SlideshowSetting {
	return f.slideshowMutations()
}
