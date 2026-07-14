package samsung

import (
	"context"
	"crypto/sha256"
	"os"
)

type mutationTransport interface {
	observationTransport
	artMutationTransport
	settingMutationTransport
	powerMutationTransport
}

type artMutationTransport interface {
	Upload(context.Context, preparedUpload) (string, error)
	Delete(context.Context, string) error
	Select(context.Context, string) error
}

type settingMutationTransport interface {
	Slideshow(context.Context) (SlideshowSetting, error)
	ConfigureSlideshow(context.Context, SlideshowSetting) error
	Brightness(context.Context) (int, error)
	ConfigureBrightness(context.Context, int) error
}

type powerMutationTransport interface {
	Wake(context.Context) error
	PowerOff(context.Context) error
}

type preparedUpload struct {
	file     *os.File
	fileType string
	matte    string
	size     int64
	digest   [sha256.Size]byte
}
