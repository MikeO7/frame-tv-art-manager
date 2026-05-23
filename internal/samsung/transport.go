package samsung

import (
	"context"
)

// Connect establishes a connection to the TV.
func (c *Client) Connect(ctx context.Context) error {
	return c.connect(ctx)
}

// Model returns the connected TV model name, if known.
func (c *Client) Model() string {
	if c.info != nil {
		return c.info.ModelName
	}
	return ""
}

// IsInArtMode reports whether the TV is currently in art mode.
func (c *Client) IsInArtMode(ctx context.Context) bool {
	return c.isInArtMode(ctx)
}

// SaveMetadata exports TV metadata to disk for auditing.
func (c *Client) SaveMetadata(ctx context.Context) error {
	return c.saveMetadata(ctx)
}

// ListUploaded returns user-uploaded artwork on the TV.
func (c *Client) ListUploaded(ctx context.Context) ([]ArtContent, error) {
	return c.getUploadedImages(ctx)
}

// Upload sends an image to the TV with the given matte style.
func (c *Client) Upload(ctx context.Context, filePath, fileType, matte string) (string, error) {
	return c.upload(ctx, filePath, fileType, matte)
}

// DeleteImages removes artwork from the TV by content IDs.
func (c *Client) DeleteImages(ctx context.Context, ids []string) error {
	return c.deleteImages(ctx, ids)
}

// SelectImage sets the currently displayed artwork.
func (c *Client) SelectImage(ctx context.Context, contentID string) error {
	return c.selectImage(ctx, contentID)
}

// SlideshowStatus returns the current slideshow configuration.
func (c *Client) SlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	return c.slideshowStatus(ctx)
}

// SetSlideshow updates the slideshow configuration.
func (c *Client) SetSlideshow(ctx context.Context, status SlideshowStatus) error {
	return c.setSlideshow(ctx, status)
}

// SetBrightness sets the art mode brightness.
func (c *Client) SetBrightness(ctx context.Context, val int) error {
	return c.setBrightness(ctx, val)
}

// TurnOff powers off the TV via the remote control API.
func (c *Client) TurnOff(ctx context.Context) error {
	return c.turnOff(ctx)
}
