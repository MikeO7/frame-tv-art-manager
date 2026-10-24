package optimize_test

import (
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func TestDefaultConfigMatchesProductionDefaults(t *testing.T) {
	t.Parallel()
	cfg, _, err := config.LoadEnviron([]string{"TV_IPS=192.0.2.10"})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if got, want := optimize.DefaultConfig(), cfg.OptimizeOptions(); got != want {
		t.Fatalf("test fixture DefaultConfig() =\n%+v\nproduction defaults =\n%+v", got, want)
	}
}
