package resources_test

import (
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/resources"
)

func TestDefaultControllerPublishesConfiguredPixelBound(t *testing.T) {
	controller := resources.NewDefaultController()
	want := resources.DefaultConfig().PixelWorkers
	if got := controller.PixelWorkers(); got != want {
		t.Fatalf("PixelWorkers() = %d, want %d", got, want)
	}
	if want < 1 || want > 4 {
		t.Fatalf("default pixel worker bound = %d, want within [1, 4]", want)
	}
}
