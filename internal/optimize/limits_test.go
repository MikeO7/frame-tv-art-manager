package optimize

import "testing"

func TestValidateInputDimensions(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		wantError     bool
	}{
		{name: "display", width: 3840, height: 2160},
		{name: "limit", width: 8000, height: 5000},
		{name: "over limit", width: 8001, height: 5000, wantError: true},
		{name: "zero", width: 0, height: 2160, wantError: true},
		{name: "negative", width: -1, height: 2160, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateInputDimensions(test.width, test.height)
			if (err != nil) != test.wantError {
				t.Fatalf("validateInputDimensions(%d, %d) error = %v", test.width, test.height, err)
			}
		})
	}
}

func TestValidateOutputDimensionsEnforcesWorkingSetAdmission(t *testing.T) {
	t.Parallel()
	if err := validateOutputDimensions(3840, 2160, 12_000_000); err != nil {
		t.Fatalf("4K target rejected: %v", err)
	}
	if err := validateOutputDimensions(16384, 16384, 12_000_000); err == nil {
		t.Fatal("unsafe target dimensions were accepted")
	}
}

func TestValidateWorkingSetRejectsUnsafeLinearTransform(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxWorkingBytes = 128 * 1024 * 1024
	if err := validateWorkingSet(8000, 5000, cfg); err == nil {
		t.Fatal("unsafe working set was accepted")
	}
}
