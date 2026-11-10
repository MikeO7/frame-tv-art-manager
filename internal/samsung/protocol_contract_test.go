package samsung

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const contractCategoryID = "MY-C0002"

func TestObservedProtocolFixtureContract(t *testing.T) {
	t.Run("device info", func(t *testing.T) {
		var response deviceInfoResponse
		readProtocolFixture(t, "device-info-on.json", &response)
		if validateFrameTVDevice(response.Device) != nil || response.Device.PowerState != stringOn ||
			response.Device.ModelName != "QN55LS03D" {
			t.Fatalf("device fixture = %+v", response.Device)
		}
	})

	for _, name := range []string{"content-list-string.json", "content-list-array.json"} {
		t.Run(name, func(t *testing.T) {
			var response artResponse
			readProtocolFixture(t, name, &response)
			content, err := parseUploadedContent(response.ContentListRaw)
			if err != nil {
				t.Fatalf("parse fixture content list: %v", err)
			}
			want := []artContent{{ContentID: "MY_F0001", CategoryID: contractCategoryID}}
			if len(content) != len(want) || content[0] != want[0] {
				t.Fatalf("content fixture = %+v, want %+v", content, want)
			}
		})
	}

	t.Run("device info identity 2022", func(t *testing.T) {
		var response deviceInfoResponse
		readProtocolFixture(t, "device-info-2022.json", &response)
		if validateFrameTVDevice(response.Device) != nil ||
			response.Device.stableDeviceID() != "uuid:0f8b7a3e-sanitized" {
			t.Fatalf("2022 device fixture = %+v", response.Device)
		}
	})

	t.Run("mixed category content list", func(t *testing.T) {
		var response artResponse
		readProtocolFixture(t, "content-list-mixed-categories.json", &response)
		content, err := parseUploadedContent(response.ContentListRaw)
		if err != nil {
			t.Fatalf("parse mixed-category content list: %v", err)
		}
		want := []artContent{
			{ContentID: "MY_F0011", CategoryID: contractCategoryID},
			{ContentID: "MY_F0010", CategoryID: contractCategoryID},
		}
		if !reflect.DeepEqual(content, want) {
			t.Fatalf("user content = %+v, want %+v", content, want)
		}
	})

	t.Run("artmode settings brightness 2022", func(t *testing.T) {
		var response artResponse
		readProtocolFixture(t, "artmode-settings-2022.json", &response)
		reading, err := parseArtModeBrightness(response.DataRaw)
		if err != nil {
			t.Fatalf("parse artmode settings: %v", err)
		}
		if reading != (brightnessReading{Value: 0, Min: 0, Max: 10}) {
			t.Fatalf("brightness reading = %+v", reading)
		}
	})

	tests := []struct {
		name  string
		check func(*testing.T, artResponse)
	}{
		{
			name: "art-mode-on.json",
			check: func(t *testing.T, response artResponse) {
				t.Helper()
				if response.Value != "on" {
					t.Fatalf("art mode value = %q", response.Value)
				}
			},
		},
		{
			name: "upload-connection-object.json",
			check: func(t *testing.T, response artResponse) {
				t.Helper()
				var info connInfo
				if err := json.Unmarshal([]byte(response.ConnInfo()), &info); err != nil {
					t.Fatalf("decode connection info: %v", err)
				}
				if info.IP != "192.0.2.20" || info.Port.String() != "12345" || !info.Secured {
					t.Fatalf("connection fixture = %+v", info)
				}
			},
		},
		{
			name: "image-added.json",
			check: func(t *testing.T, response artResponse) {
				t.Helper()
				if response.ContentID != "MY_F0002" {
					t.Fatalf("added content ID = %q", response.ContentID)
				}
			},
		},
		{
			name: "storage-error.json",
			check: func(t *testing.T, response artResponse) {
				t.Helper()
				if response.ErrorCode != "507" || !errors.Is(checkArtError(&response), ErrStorageFull) {
					t.Fatalf("storage error code = %q", response.ErrorCode)
				}
			},
		},
		{
			// Captured from a 2022 Frame (QN43LS03B) by samsung-tv-ws-api.
			name: "send-image-error-2022.json",
			check: func(t *testing.T, response artResponse) {
				t.Helper()
				if response.ID == "" || !errors.Is(checkArtError(&response), ErrArtAPIError) {
					t.Fatalf("real TV error envelope = %+v", response)
				}
			},
		},
		{
			name: "upload-connection-secured-2022.json",
			check: func(t *testing.T, response artResponse) {
				t.Helper()
				var info connInfo
				if err := json.Unmarshal([]byte(response.ConnInfo()), &info); err != nil {
					t.Fatalf("decode secured connection info: %v", err)
				}
				if info.Port.String() != "51317" || !info.Secured || info.Key != "tmvo5fr7" {
					t.Fatalf("secured connection fixture = %+v", info)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response artResponse
			readProtocolFixture(t, test.name, &response)
			test.check(t, response)
		})
	}
}

func readProtocolFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "protocol", name))
	if err != nil {
		t.Fatalf("read protocol fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode protocol fixture %s: %v", name, err)
	}
}
