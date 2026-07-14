package samsung

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const contractCategoryID = "MY-C0002"

func TestObservedProtocolFixtureContract(t *testing.T) {
	t.Run("device info", func(t *testing.T) {
		var response deviceInfoResponse
		readProtocolFixture(t, "device-info-on.json", &response)
		if !response.Device.IsFrameTV() || !response.Device.IsOn() ||
			response.Device.ModelName != "QN55LS03D" {
			t.Fatalf("device fixture = %+v", response.Device)
		}
	})

	for _, name := range []string{"content-list-string.json", "content-list-array.json"} {
		t.Run(name, func(t *testing.T) {
			var response artResponse
			readProtocolFixture(t, name, &response)
			var content []ArtContent
			if err := json.Unmarshal([]byte(response.ContentList()), &content); err != nil {
				t.Fatalf("decode fixture content list: %v", err)
			}
			want := []ArtContent{{ContentID: "MY_F0001", CategoryID: contractCategoryID}}
			if len(content) != len(want) || content[0] != want[0] {
				t.Fatalf("content fixture = %+v, want %+v", content, want)
			}
		})
	}

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
				if response.ErrorCode != 507 {
					t.Fatalf("storage error code = %d", response.ErrorCode)
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
