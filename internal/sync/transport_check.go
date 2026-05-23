package sync

import "github.com/MikeO7/frame-tv-art-manager/internal/samsung"

var _ TVTransport = (*samsung.Client)(nil)
