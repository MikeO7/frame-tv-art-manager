package sync

import (
	"testing"
)

func TestMatteConfig_String(t *testing.T) {
	tests := []struct {
		name         string
		overrides    map[string]string
		defaultMatte string
		want         string
	}{
		{
			name:      "empty",
			overrides: nil,
			want:      "global (no per-file overrides)",
		},
		{
			name:         "only default",
			defaultMatte: "none",
			want:         "0 per-file overrides, default=\"none\"",
		},
		{
			name: "overrides and default",
			overrides: map[string]string{
				"art1.jpg": "matte1",
			},
			defaultMatte: "matte2",
			want:         "1 per-file overrides, default=\"matte2\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MatteConfig{
				overrides:    tt.overrides,
				defaultMatte: tt.defaultMatte,
			}
			if got := mc.String(); got != tt.want {
				t.Errorf("MatteConfig.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
