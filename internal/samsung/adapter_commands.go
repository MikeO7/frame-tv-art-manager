package samsung

import "crypto/sha256"

// Upload binds a committed artwork file's immutable metadata to one command.
type Upload struct {
	Path     string
	Name     string
	FileType string
	Matte    string
	Digest   [sha256.Size]byte
	Size     int64
}

func (Upload) isSamsungCommand() {}

// Delete removes exactly one freshly observed content ID.
type Delete struct{ ContentID string }

func (Delete) isSamsungCommand() {}

// Select displays exactly one freshly observed content ID.
type Select struct{ ContentID string }

func (Select) isSamsungCommand() {}

// ConfigureSlideshow changes a complete known slideshow setting.
type ConfigureSlideshow struct {
	Previous SlideshowSetting
	Desired  SlideshowSetting
}

func (ConfigureSlideshow) isSamsungCommand() {}

// ConfigureBrightness changes a known previous level to Value.
type ConfigureBrightness struct {
	PreviousValue int
	Value         int
}

func (ConfigureBrightness) isSamsungCommand() {}
