package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SaveMetadata fetches device info, slideshow status, and artwork categories,
// writing them to a per-TV JSON file in the tokens directory for auditing.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the network requests.
//
// Returns:
//   - error: Any network, serialization, or filesystem error encountered during save.
func (c *Client) SaveMetadata(ctx context.Context) error {
	metadata := make(map[string]any)
	metadata["timestamp"] = time.Now().Format(time.RFC3339)

	// 1. Basic Device Info.
	if c.info != nil {
		metadata["device"] = c.info
	}

	// 2. Slideshow Status.
	if ss, err := c.SlideshowStatus(ctx); err == nil {
		metadata["slideshow"] = ss
	}

	// 3. All Categories.
	if cats, err := c.getCategories(ctx); err == nil {
		var raw json.RawMessage
		if err := json.Unmarshal(cats, &raw); err == nil {
			metadata["categories"] = raw
		}
	}

	// 4. Detailed Environment (reserved for future telemetry integration).
	metadata["platform"] = "Y2025"

	b, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	path := filepath.Join(c.opts.TokenDir, fmt.Sprintf("tv_%s_metadata.json", safeIP))

	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}

	c.logger.Info("metadata saved", "path", path)
	return nil
}

// SlideshowStatus returns the TV's current slideshow configuration.
func (c *Client) SlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_slideshow_status",
		"id":         id,
		keyRequestID: id,
	}

	_, raw, err := c.sendArtRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Value      string `json:"value"`
		Type       string `json:"type"`
		CategoryID string `json:"category_id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse slideshow_status: %w", err)
	}

	return &SlideshowStatus{
		Value:      resp.Value,
		Type:       resp.Type,
		CategoryID: resp.CategoryID,
	}, nil
}

// SetSlideshow updates the TV's slideshow configuration.
func (c *Client) SetSlideshow(ctx context.Context, s SlideshowStatus) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:    "set_slideshow_status",
		"id":          id,
		keyRequestID:  id,
		"value":       s.Value,
		"category_id": s.CategoryID,
		"type":        s.Type,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// SetBrightness sets the art-mode brightness value.
func (c *Client) SetBrightness(ctx context.Context, val int) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "set_brightness",
		"id":         id,
		keyRequestID: id,
		"value":      val,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}
